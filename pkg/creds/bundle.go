// Package creds builds the systemd credentials bundle Booty serves to
// Bluefin Server installs: a tar of *.cred files the installer unpacks into
// the new ESP's /loader/credentials/, where systemd-stub hands them to the
// booted UKI as encrypted system credentials. It carries the same hostname,
// SSH keys and Booty units the Ignition builtin fragment does for
// Flatcar/CoreOS, so the toggles are shared (--builtin).
//
// Every credential is encrypted with the systemd "null" key (see Encrypt):
// systemd only imports /loader/credentials/*.cred through
// /run/credentials/@encrypted, and plaintext files there fail with
// "Failed to set up credentials: Invalid argument".
//
// Null-key credentials are only decrypted for ImportCredential= consumers
// (systemd-tmpfiles, sysusers, the network generator, udev): those read with
// CREDENTIAL_ALLOW_NULL. PID 1 and the generators do not, so
// systemd.extra-unit.*, systemd.unit-dropin.* and system.hostname fail with
// "Operation not supported" (verified on Bluefin Server 26.08.0, systemd
// 257, no TPM, Secure Boot off). Everything Booty wants on the node
// therefore travels inside tmpfiles.extra: files, unit files under
// /etc/systemd/system plus the .wants/ symlinks systemctl enable would
// have made, /etc/hostname, and under --clusterDistribution=k0s the node's
// k0s pieces (pkg/cluster/k0s): k0s.yaml, PKI, token, manifests, the
// ready/CNI units and the ExecStart= drop-in that turns the image's
// k0scontroller.service into Booty's controller or worker command.
package creds

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k0s"
	ign "github.com/jeefy/booty/pkg/ignition"
)

const (
	// HostnameCredential is read by systemd-firstboot. The installer-v26.08.0
	// image ships bluefin-firstboot-credentials.service but not the
	// systemd-firstboot binary, so it is a no-op there; it stays in the
	// bundle for images that have it. TmpfilesRules carries the hostname too.
	HostnameCredential = "firstboot.hostname"
	// TmpfilesCredential is applied by systemd-tmpfiles-setup.service on
	// every boot (ImportCredential=tmpfiles.*).
	TmpfilesCredential = "tmpfiles.extra"

	// HostnameUnitName is the Bluefin-only oneshot that copies /etc/hostname
	// into the kernel, see hostnameUnit.
	HostnameUnitName = "booty-hostname.service"

	credSuffix   = ".cred"
	credMode     = 0o600
	unitDir      = "/etc/systemd/system"
	hostnamePath = "/etc/hostname"

	// firstBootHook is the unit whose start pulls Booty's units in on the
	// very first boot. Bluefin Server's k0s-first-boot.service (every boot,
	// WantedBy=multi-user.target) ends with "systemctl daemon-reload" and
	// "systemctl enable --now k0scontroller.service". The reload makes the
	// units tmpfiles wrote visible, but multi-user.target's job graph was
	// computed before tmpfiles ran, so the .wants/ symlinks alone leave them
	// loaded and enabled yet never started until the next boot. The
	// k0scontroller job is enqueued after the reload, so a Wants= drop-in on
	// it starts them. From the second boot on the symlinks suffice and the
	// drop-in is redundant (the units are already active).
	firstBootHook = "k0scontroller.service"
)

// hostnameUnit applies /etc/hostname to the running kernel. PID 1 only
// reads /etc/hostname at boot, before tmpfiles has written it, so on the
// very first boot the name is set by this unit instead -- and only once the
// image's k0s-first-boot.service daemon-reload has made the unit visible,
// so first-boot DHCP may still announce "localhost". From the second boot on
// PID 1 applies the file itself and this unit is a no-op.
const hostnameUnit = `[Unit]
Description=Apply Booty hostname
DefaultDependencies=no
Before=network-pre.target
ConditionPathExists=/etc/hostname

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'h=$(cat /etc/hostname); [ "$(cat /proc/sys/kernel/hostname)" = "$h" ] || echo "$h" > /proc/sys/kernel/hostname'

[Install]
WantedBy=sysinit.target
`

// Input is everything the bundle depends on. Server is the host[:port]
// clients use to reach Booty; K0s is the host's k0s node (nil: none).
type Input struct {
	Hostname string
	Server   string
	SSHKeys  []string
	K0s      *k0s.Node
}

// Bundle returns the tar of encrypted credentials for in, restricted to the
// enabled features. The output is byte-for-byte deterministic (fixed mtime,
// uid/gid 0, mode 0600, sorted names, USTAR, deterministic IVs) so Sum of it
// can be embedded in the iPXE script before the installer downloads the
// same bytes.
func Bundle(in Input, f ign.Features) ([]byte, error) {
	files := Credentials(in, f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range names {
		body, err := Encrypt(name, []byte(files[name]))
		if err != nil {
			return nil, fmt.Errorf("credential %s: %w", name, err)
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name + credSuffix,
			Mode:     credMode,
			Size:     int64(len(body)),
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("credential %s: %w", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			return nil, fmt.Errorf("credential %s: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Sum is the lowercase hex SHA-256 of data, the value of inst.creds_sha256.
func Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Credentials maps credential names (without .cred) to their plaintext
// contents; Bundle encrypts them. The set is at most firstboot.hostname and
// tmpfiles.extra.
func Credentials(in Input, f ign.Features) map[string]string {
	out := map[string]string{}
	if hasHostname(in, f) {
		out[HostnameCredential] = in.Hostname + "\n"
	}
	if rules := TmpfilesRules(in, f); rules != "" {
		out[TmpfilesCredential] = rules
	}
	return out
}

// TmpfilesRules renders the tmpfiles.extra credential: systemd-tmpfiles(5)
// lines that write /etc/hostname, the SSH keys, Booty's units and the
// update-check script. "f+" overwrites, "f~" creates with base64 contents,
// "L+" replaces an existing symlink.
//
// Units land in /etc/systemd/system with the .wants/ symlink systemctl
// enable would create, plus a Wants= drop-in on firstBootHook so they also
// start on the first boot; every later boot loads them from /etc like any
// other unit. The k0s node's files and units follow the same rules (and,
// like --profile, are off under --builtin=none); its drop-in on
// k0scontroller.service sits next to the Wants= one.
func TmpfilesRules(in Input, f ign.Features) string {
	var b strings.Builder
	var units []string
	var dropIns []k0s.DropIn
	if hasHostname(in, f) {
		fmt.Fprintf(&b, "f+ %s 0644 root root - %s\n", hostnamePath, in.Hostname)
		writeUnit(&b, HostnameUnitName, hostnameUnit, "sysinit.target")
		units = append(units, HostnameUnitName)
	}
	if f[ign.FeatureSSHKeys] && len(in.SSHKeys) > 0 {
		b.WriteString("d /home/core/.ssh 0700 core core -\n")
		writeFile(&b, "/home/core/.ssh/authorized_keys", "0600", "core", strings.Join(in.SSHKeys, "\n")+"\n")
	}
	if f[ign.FeatureBooted] {
		writeUnit(&b, ign.BootedUnitName, ign.BootedUnit(in.Server), "multi-user.target")
		units = append(units, ign.BootedUnitName)
	}
	if f[ign.FeatureUpdate] {
		writeUnit(&b, ign.UpdateServiceName, ign.UpdateService, "")
		writeUnit(&b, ign.UpdateTimerName, ign.UpdateTimer, "timers.target")
		writeFile(&b, ign.UpdateCheckScriptPath, "0755", "root", ign.UpdateCheckScript(in.Server))
		units = append(units, ign.UpdateTimerName)
	}
	if in.K0s != nil && f.Enabled() {
		for _, dir := range k0sDirs(in.K0s) {
			fmt.Fprintf(&b, "d %s 0755 root root -\n", dir)
		}
		for _, file := range in.K0s.Files {
			writeFile(&b, file.Path, fmt.Sprintf("%04o", file.Mode), "root", file.Contents)
		}
		for _, u := range in.K0s.Units {
			writeUnit(&b, u.Name, u.Contents, u.WantedBy)
			units = append(units, u.Name)
		}
		dropIns = in.K0s.DropIns
	}
	if len(units) > 0 {
		dropIns = append(dropIns, k0s.DropIn{Unit: firstBootHook, Name: "booty.conf", Contents: "[Unit]\nWants=" + strings.Join(units, " ") + "\n"})
	}
	writeDropIns(&b, dropIns)
	return b.String()
}

// k0sDirs lists the parent directories of the node's files under /etc/k0s
// and /var/lib/k0s, sorted so parents come first.
func k0sDirs(n *k0s.Node) []string {
	seen := map[string]bool{}
	for _, f := range n.Files {
		dir := path.Dir(f.Path)
		if !strings.HasPrefix(dir, k0s.ConfigDir) && !strings.HasPrefix(dir, k0s.DataDir) {
			continue
		}
		for d := dir; d != "/" && d != "/etc" && d != "/var/lib"; d = path.Dir(d) {
			seen[d] = true
		}
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

func writeDropIns(b *strings.Builder, dropIns []k0s.DropIn) {
	byUnit := map[string][]k0s.DropIn{}
	var order []string
	for _, d := range dropIns {
		if _, ok := byUnit[d.Unit]; !ok {
			order = append(order, d.Unit)
		}
		byUnit[d.Unit] = append(byUnit[d.Unit], d)
	}
	for _, unit := range order {
		dir := unitDir + "/" + unit + ".d"
		fmt.Fprintf(b, "d %s 0755 root root -\n", dir)
		for _, d := range byUnit[unit] {
			writeFile(b, dir+"/"+d.Name, "0644", "root", d.Contents)
		}
	}
}

func hasHostname(in Input, f ign.Features) bool {
	return f[ign.FeatureHostname] && in.Hostname != ""
}

func writeFile(b *strings.Builder, path, mode, owner, contents string) {
	fmt.Fprintf(b, "f~ %s %s %s %s - %s\n", path, mode, owner, owner, base64.StdEncoding.EncodeToString([]byte(contents)))
}

func writeUnit(b *strings.Builder, name, contents, wantedBy string) {
	path := unitDir + "/" + name
	writeFile(b, path, "0644", "root", contents)
	if wantedBy != "" {
		fmt.Fprintf(b, "L+ %s/%s.wants/%s - - - - %s\n", unitDir, wantedBy, name, path)
	}
}
