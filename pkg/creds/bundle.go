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
package creds

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	ign "github.com/jeefy/booty/pkg/ignition"
)

const (
	HostnameCredential = "firstboot.hostname"
	TmpfilesCredential = "tmpfiles.extra"

	// ExtraUnitPrefix and UnitDropinPrefix are the systemd-debug-generator(8)
	// credentials that add a unit file, respectively a drop-in for one, to the
	// booted system. Units written to /etc/systemd/system by tmpfiles.extra
	// are not loaded (tmpfiles runs after the unit tree is read), and the
	// generator ignores [Install], so each unit gets a Wants= drop-in on the
	// target that would have enabled it.
	ExtraUnitPrefix  = "systemd.extra-unit."
	UnitDropinPrefix = "systemd.unit-dropin."
	dropinName       = "booty"

	credSuffix = ".cred"
	credMode   = 0o600
)

// Input is everything the bundle depends on. Server is the host[:port]
// clients use to reach Booty.
type Input struct {
	Hostname string
	Server   string
	SSHKeys  []string
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
// contents; Bundle encrypts them.
func Credentials(in Input, f ign.Features) map[string]string {
	out := map[string]string{}
	if f[ign.FeatureHostname] && in.Hostname != "" {
		out[HostnameCredential] = in.Hostname + "\n"
	}
	if rules := TmpfilesRules(in, f); rules != "" {
		out[TmpfilesCredential] = rules
	}
	wants := map[string][]string{}
	addUnit := func(name, contents, wantedBy string) {
		out[ExtraUnitPrefix+name] = contents
		if wantedBy != "" {
			wants[wantedBy] = append(wants[wantedBy], name)
		}
	}
	if f[ign.FeatureBooted] {
		addUnit(ign.BootedUnitName, ign.BootedUnit(in.Server), "multi-user.target")
	}
	if f[ign.FeatureUpdate] {
		addUnit(ign.UpdateServiceName, ign.UpdateService, "")
		addUnit(ign.UpdateTimerName, ign.UpdateTimer, "timers.target")
	}
	for target, units := range wants {
		out[UnitDropinPrefix+target+"~"+dropinName] = "[Unit]\nWants=" + strings.Join(units, " ") + "\n"
	}
	return out
}

// TmpfilesRules renders the tmpfiles.extra credential: systemd-tmpfiles(5)
// lines that write the SSH keys and the update-check script on first boot.
// "f~" takes the file contents base64 encoded.
func TmpfilesRules(in Input, f ign.Features) string {
	var b strings.Builder
	if f[ign.FeatureSSHKeys] && len(in.SSHKeys) > 0 {
		b.WriteString("d /home/core/.ssh 0700 core core -\n")
		writeFile(&b, "/home/core/.ssh/authorized_keys", "0600", "core", strings.Join(in.SSHKeys, "\n")+"\n")
	}
	if f[ign.FeatureUpdate] {
		writeFile(&b, ign.UpdateCheckScriptPath, "0755", "root", ign.UpdateCheckScript(in.Server))
	}
	return b.String()
}

func writeFile(b *strings.Builder, path, mode, owner, contents string) {
	fmt.Fprintf(b, "f~ %s %s %s %s - %s\n", path, mode, owner, owner, base64.StdEncoding.EncodeToString([]byte(contents)))
}
