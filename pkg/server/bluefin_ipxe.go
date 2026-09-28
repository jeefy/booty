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

// ukiCmdline reads the embedded kernel command line of the UKI at path:
// its PE .cmdline section without the trailing NUL padding.
func ukiCmdline(path string) (string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	defer config.CloseQuietly(f, path)
	s := f.Section(ukiCmdlineName)
	if s == nil {
		return "", fmt.Errorf("%s: no %s section", path, ukiCmdlineName)
	}
	size := s.Size
	if s.VirtualSize > 0 && s.VirtualSize < size {
		size = s.VirtualSize
	}
	if size > maxUKICmdlineLen {
		return "", fmt.Errorf("%s: %s section of %d bytes is implausibly large", path, ukiCmdlineName, size)
	}
	data := make([]byte, size)
	if _, err := s.ReadAt(data, 0); err != nil {
		return "", fmt.Errorf("%s: reading %s: %w", path, ukiCmdlineName, err)
	}
	cmdline := strings.TrimRight(string(bytes.TrimRight(data, "\x00")), " \t\r\n")
	if cmdline == "" {
		return "", fmt.Errorf("%s: empty %s section", path, ukiCmdlineName)
	}
	return cmdline, nil
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

type ukiCmdlineEntry struct {
	cmdline string
	err     error
}

// ukiCmdlineCache holds the embedded cmdline per netboot UKI (version,
// sha256, size and mtime), so the UKI is parsed once per release.
var ukiCmdlineCache = struct {
	sync.Mutex
	m map[string]ukiCmdlineEntry
}{m: map[string]ukiCmdlineEntry{}}

func cachedUKICmdline(m versions.BluefinManifest) (string, error) {
	path := config.DataPath(config.BluefinDir, m.Version, m.NetbootUKI)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s|%s|%d|%d", m.Version, m.SHA256Sums[m.NetbootUKI], info.Size(), info.ModTime().UnixNano())
	ukiCmdlineCache.Lock()
	defer ukiCmdlineCache.Unlock()
	if e, ok := ukiCmdlineCache.m[key]; ok {
		return e.cmdline, e.err
	}
	cmdline, err := ukiCmdline(path)
	if len(ukiCmdlineCache.m) >= 8 {
		clear(ukiCmdlineCache.m)
	}
	ukiCmdlineCache.m[key] = ukiCmdlineEntry{cmdline: cmdline, err: err}
	return cmdline, err
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

// bluefinIPXEVars fills the Bluefin part of the /booty.ipxe render. It
// records nothing: the chainloaded UKI's GET does the boot bookkeeping.
func bluefinIPXEVars(mac string, host *hardware.Host, secureBoot bool, vars *tftp.TemplateVars) {
	vars.BluefinBootURL = bluefinBootURL(mac)
	if host.Installed() && !host.DoInstall {
		vars.BluefinInstalled = true
		return
	}
	if secureBoot {
		vars.BluefinReason = reasonSecureBoot
		return
	}
	m, ok := versions.CurrentBluefinManifest()
	if !ok {
		vars.BluefinReason = reasonNoRelease
		return
	}
	cmdline, err := bluefinChainCmdline(mac, m)
	if err != nil {
		slog.Error("Cannot chainload the Bluefin netboot UKI from iPXE; serving the UEFI HTTP Boot menu", "mac", mac, "version", m.Version, "error", err)
		vars.BluefinReason = reasonNoCmdline
		return
	}
	vars.BluefinVersion = m.Version
	vars.BluefinChainURL = bluefinChainURL(mac, m.Version)
	vars.BluefinChainCmdline = cmdline
}

var errNoUKI = errors.New("not a netboot UKI of the current or previous release")

// bluefinUKIFor resolves the requested *.efi name: the versioned name of
// the current or previous release's netboot UKI selects that release, any
// other bluefin-server-netboot_*.efi is refused, and every other name is
// the current UKI (the UEFI HTTP Boot path).
func bluefinUKIFor(name string) (link, file string, err error) {
	if !strings.HasPrefix(name, "bluefin-server-netboot_") {
		m, ok := versions.CurrentBluefinManifest()
		if !ok {
			return "", "", errNoUKI
		}
		return config.BluefinCurrentLink, m.NetbootUKI, nil
	}
	for _, link := range []string{config.BluefinCurrentLink, config.BluefinPreviousLink} {
		if m, ok := bluefinManifest(link); ok && m.NetbootUKI == name {
			return link, name, nil
		}
	}
	return "", "", errNoUKI
}
