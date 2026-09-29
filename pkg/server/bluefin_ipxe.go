package server

import (
	"bytes"
	"debug/pe"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/jeefy/booty/pkg/versions"
)

const (
	pullArgPrefix    = "rd.systemd.pull="
	pullBootOrigin   = "bootorigin"
	ukiCmdlineName   = ".cmdline"
	maxUKICmdlineLen = 64 << 10
)

// ukiPayload is where a PE section's payload sits in the UKI file.
type ukiPayload struct {
	offset, size int64
}

// ukiInfo is what Booty needs from a netboot UKI: its section payloads
// (served individually on the BIOS path) and its embedded command line.
type ukiInfo struct {
	sections   map[string]ukiPayload
	cmdline    string
	cmdlineErr error
}

// payloadSize is a section's payload length: VirtualSize is the real size
// (systemd-ukify pads SizeOfRawData to the file alignment), but never more
// than what is in the file; 0 means unset.
func payloadSize(s *pe.Section) int64 {
	if s.VirtualSize != 0 && s.VirtualSize < s.Size {
		return int64(s.VirtualSize)
	}
	return int64(s.Size)
}

// readUKI reads the section table of the UKI at path and its .cmdline
// section, without the trailing NUL padding.
func readUKI(path string) (*ukiInfo, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer config.CloseQuietly(fh, path)
	info, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	f, err := pe.NewFile(fh)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	u := &ukiInfo{sections: map[string]ukiPayload{}}
	for _, s := range f.Sections {
		p := ukiPayload{offset: int64(s.Offset), size: payloadSize(s)}
		if p.offset < 0 || p.offset+p.size > info.Size() {
			return nil, fmt.Errorf("%s: section %s lies outside the file", path, s.Name)
		}
		u.sections[s.Name] = p
	}
	u.cmdline, u.cmdlineErr = sectionCmdline(fh, path, u.sections)
	return u, nil
}

func sectionCmdline(fh *os.File, path string, sections map[string]ukiPayload) (string, error) {
	p, ok := sections[ukiCmdlineName]
	if !ok {
		return "", fmt.Errorf("%s: no %s section", path, ukiCmdlineName)
	}
	if p.size > maxUKICmdlineLen {
		return "", fmt.Errorf("%s: %s section of %d bytes is implausibly large", path, ukiCmdlineName, p.size)
	}
	data := make([]byte, p.size)
	if _, err := fh.ReadAt(data, p.offset); err != nil {
		return "", fmt.Errorf("%s: reading %s: %w", path, ukiCmdlineName, err)
	}
	cmdline := strings.TrimRight(string(bytes.TrimRight(data, "\x00")), " \t\r\n")
	if cmdline == "" {
		return "", fmt.Errorf("%s: empty %s section", path, ukiCmdlineName)
	}
	return cmdline, nil
}

// ukiCmdline reads the embedded kernel command line of the UKI at path.
func ukiCmdline(path string) (string, error) {
	u, err := readUKI(path)
	if err != nil {
		return "", err
	}
	return u.cmdline, u.cmdlineErr
}

// argSpans returns the [start, end) byte ranges of the whitespace-separated
// kernel arguments in cmdline; double quotes group, as the kernel parses.
func argSpans(cmdline string) [][2]int {
	var spans [][2]int
	start, quoted := -1, false
	for i := 0; i < len(cmdline); i++ {
		c := cmdline[i]
		switch {
		case c == '"':
			quoted = !quoted
			if start < 0 {
				start = i
			}
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if start >= 0 {
				spans = append(spans, [2]int{start, i})
				start = -1
			}
		case start < 0:
			start = i
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(cmdline)})
	}
	return spans
}

// rewriteBootOriginPull turns the single rd.systemd.pull argument that
// pulls relative to the boot origin (<opts incl. bootorigin>:<local>:<name>)
// into one pulling <base>/<name> explicitly, and returns the cmdline with
// every other byte unchanged and the pulled file name. iPXE sets no
// StubDeviceURL, so bootorigin pulls are skipped when the UKI is
// chainloaded.
func rewriteBootOriginPull(cmdline, base string) (string, string, error) {
	var found [][2]int
	for _, sp := range argSpans(cmdline) {
		arg := cmdline[sp[0]:sp[1]]
		if !strings.HasPrefix(arg, pullArgPrefix) {
			continue
		}
		opts, _, _ := strings.Cut(strings.TrimPrefix(arg, pullArgPrefix), ":")
		for _, o := range strings.Split(opts, ",") {
			if o == pullBootOrigin {
				found = append(found, sp)
				break
			}
		}
	}
	if len(found) != 1 {
		return "", "", fmt.Errorf("want exactly one %s argument with %s, found %d", pullArgPrefix, pullBootOrigin, len(found))
	}
	sp := found[0]
	value := strings.TrimPrefix(cmdline[sp[0]:sp[1]], pullArgPrefix)
	opts, rest, ok := strings.Cut(value, ":")
	local, name, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || local == "" || name == "" || strings.ContainsAny(name, "/:\"") {
		return "", "", fmt.Errorf("%s%s: not <options>:<local name>:<file name>", pullArgPrefix, value)
	}
	var kept []string
	for _, o := range strings.Split(opts, ",") {
		if o != pullBootOrigin {
			kept = append(kept, o)
		}
	}
	arg := pullArgPrefix + strings.Join(kept, ",") + ":" + local + ":" + strings.TrimSuffix(base, "/") + "/" + name
	return cmdline[:sp[0]] + arg + cmdline[sp[1]:], name, nil
}

// ipxeSafeArgs refuses what iPXE would expand or re-split on the chain line.
func ipxeSafeArgs(s string) error {
	for _, c := range s {
		if c < 0x20 || c == 0x7f || strings.ContainsRune("$\"'\\", c) {
			return fmt.Errorf("character %q cannot be passed through an iPXE chain line", c)
		}
	}
	return nil
}

type cachedUKIEntry struct {
	uki *ukiInfo
	err error
}

// ukiCache holds the parsed UKI per release file (version, sha256, size and
// mtime), so a UKI is parsed once per release.
var ukiCache = struct {
	sync.Mutex
	m map[string]cachedUKIEntry
}{m: map[string]cachedUKIEntry{}}

// bluefinUKIPath is the netboot UKI of release m.
func bluefinUKIPath(m versions.BluefinManifest) string {
	return config.DataPath(config.BluefinDir, m.Version, m.NetbootUKI)
}

// cachedUKI returns release m's parsed netboot UKI and the file's stat.
func cachedUKI(m versions.BluefinManifest) (*ukiInfo, os.FileInfo, error) {
	path := bluefinUKIPath(m)
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	key := fmt.Sprintf("%s|%s|%d|%d", m.Version, m.SHA256Sums[m.NetbootUKI], info.Size(), info.ModTime().UnixNano())
	ukiCache.Lock()
	defer ukiCache.Unlock()
	if e, ok := ukiCache.m[key]; ok {
		return e.uki, info, e.err
	}
	uki, err := readUKI(path)
	if len(ukiCache.m) >= 8 {
		clear(ukiCache.m)
	}
	ukiCache.m[key] = cachedUKIEntry{uki: uki, err: err}
	return uki, info, err
}

func cachedUKICmdline(m versions.BluefinManifest) (string, error) {
	uki, _, err := cachedUKI(m)
	if err != nil {
		return "", err
	}
	return uki.cmdline, uki.cmdlineErr
}

// bluefinChainURL names the release's netboot UKI explicitly, so the UKI
// iPXE fetches always matches the command line rendered for it, even when
// the served release changes in between.
func bluefinChainURL(mac, version string) string {
	return bluefinDirURL(mac) + "/" + versions.BluefinNetbootUKI(version)
}

func bluefinDirURL(mac string) string {
	return "http://" + config.ServerHostPort() + bluefinPathPrefix + strings.ReplaceAll(mac, ":", "-")
}

// bluefinChainCmdline derives the command line iPXE passes to the current
// netboot UKI for mac: the UKI's own .cmdline with its bootorigin pull
// pointed at /bluefin/<mac>/, where the initrd also finds
// bluefin-node.ign. Any doubt is an error, and the caller falls back to
// the UEFI HTTP Boot menu.
func bluefinChainCmdline(mac string, m versions.BluefinManifest) (string, error) {
	cmdline, err := cachedUKICmdline(m)
	if err != nil {
		return "", err
	}
	rewritten, name, err := rewriteBootOriginPull(cmdline, bluefinDirURL(mac))
	if err != nil {
		return "", err
	}
	if name != m.DDI {
		return "", fmt.Errorf("the UKI pulls %s, but release %s serves %s", name, m.Version, m.DDI)
	}
	if err := ipxeSafeArgs(rewritten); err != nil {
		return "", err
	}
	return rewritten, nil
}

var (
	reasonSecureBoot = []string{
		"with Secure Boot on, systemd-stub ignores the command line iPXE passes, so the UKI would",
		"not find its OS image. Keep Secure Boot and use UEFI HTTP Boot, or disable Secure Boot.",
	}
	reasonNoRelease = []string{"no Bluefin Server release is cached yet."}
	reasonNoCmdline = []string{"the current release's netboot UKI has no usable command line (see Booty's log)."}
)

// bluefinIPXEVars fills the Bluefin part of the /booty.ipxe render: the
// EFI branch (chainload with Secure Boot off, the UEFI HTTP Boot menu
// otherwise) and the legacy BIOS branch, which Secure Boot does not affect.
// Both use the same derived command line and fail closed the same way. It
// records nothing: the UKI or kernel GET does the boot bookkeeping.
func bluefinIPXEVars(mac string, host *hardware.Host, secureBoot bool, vars *tftp.TemplateVars) {
	vars.BluefinBootURL = bluefinBootURL(mac)
	if host.Installed() && !host.DoInstall {
		vars.BluefinInstalled = true
		return
	}
	m, haveRelease := bluefinReleaseFor(host)
	var cmdline string
	var cmdlineErr error
	if haveRelease {
		if cmdline, cmdlineErr = bluefinChainCmdline(mac, m); cmdlineErr != nil {
			slog.Error("Cannot derive the Bluefin netboot command line; serving the UEFI HTTP Boot and BIOS fallback menus", "mac", mac, "version", m.Version, "error", cmdlineErr)
		}
	}
	switch {
	case secureBoot:
		vars.BluefinReason = reasonSecureBoot
	case !haveRelease:
		vars.BluefinReason = reasonNoRelease
	case cmdlineErr != nil:
		vars.BluefinReason = reasonNoCmdline
	default:
		vars.BluefinVersion = m.Version
		vars.BluefinChainURL = bluefinChainURL(mac, m.Version)
		vars.BluefinChainCmdline = cmdline
	}
	vars.BluefinBIOS = bluefinBIOSVars(mac, host, m, haveRelease, cmdline, cmdlineErr)
}

var reasonNoBIOSKernel = []string{"the current release's netboot UKI has no .linux or .initrd section (see Booty's log)."}

// bluefinBIOSVars fills the BIOS branch: release m's .linux booted with
// cmdline, its .ucode (when present) and .initrd as initrds.
func bluefinBIOSVars(mac string, host *hardware.Host, m versions.BluefinManifest, haveRelease bool, cmdline string, cmdlineErr error) tftp.BluefinBIOS {
	b := tftp.BluefinBIOS{InstallPending: host.DoInstall, InstallDisk: host.InstallDisk, PreferDisk: host.Installed()}
	switch {
	case !haveRelease:
		b.Reason = reasonNoRelease
		return b
	case cmdlineErr != nil:
		b.Reason = reasonNoCmdline
		return b
	}
	uki, _, err := cachedUKI(m)
	if err != nil {
		slog.Error("Bluefin netboot UKI unreadable", "mac", mac, "version", m.Version, "error", err)
		b.Reason = reasonNoCmdline
		return b
	}
	_, hasLinux := uki.sections[".linux"]
	_, hasInitrd := uki.sections[".initrd"]
	if !hasLinux || !hasInitrd {
		slog.Error("Bluefin netboot UKI cannot be booted in BIOS mode", "mac", mac, "version", m.Version, "linux", hasLinux, "initrd", hasInitrd)
		b.Reason = reasonNoBIOSKernel
		return b
	}
	b.Version = m.Version
	b.Cmdline = cmdline
	b.KernelURL = bluefinSectionURL(mac, m.Version, ".linux")
	if _, ok := uki.sections[".ucode"]; ok {
		b.InitrdURLs = append(b.InitrdURLs, bluefinSectionURL(mac, m.Version, ".ucode"))
	}
	b.InitrdURLs = append(b.InitrdURLs, bluefinSectionURL(mac, m.Version, ".initrd"))
	return b
}

var errNoUKI = errors.New("not a netboot UKI of a cached release")

// bluefinUKIFor resolves the requested *.efi name: the versioned name of a
// cached release's netboot UKI selects that release, any other
// bluefin-server-netboot_*.efi is refused, and every other name is the
// host's target release's UKI (the UEFI HTTP Boot path).
func bluefinUKIFor(host *hardware.Host, name string) (versions.BluefinManifest, error) {
	if !strings.HasPrefix(name, "bluefin-server-netboot_") {
		m, ok := bluefinReleaseFor(host)
		if !ok {
			return versions.BluefinManifest{}, errNoUKI
		}
		return m, nil
	}
	m, ok := bluefinManifestWhere(func(m versions.BluefinManifest) bool { return m.NetbootUKI == name })
	if !ok {
		return versions.BluefinManifest{}, errNoUKI
	}
	return m, nil
}
