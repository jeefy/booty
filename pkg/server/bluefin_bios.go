package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/versions"
)

// UKI sections served on the legacy BIOS path, by URL suffix. iPXE's BIOS
// bzImage loader boots .linux and concatenates the initrds in the order
// they were fetched, which must be .ucode (early microcode, uncompressed
// cpio) before .initrd, as systemd-stub does on EFI.
var bluefinSections = map[string]string{
	".linux":  ".linux",
	".initrd": ".initrd",
	".ucode":  ".ucode",
}

func isBluefinSectionName(name string) bool {
	for ext := range bluefinSections {
		if strings.HasPrefix(name, "bluefin-server-netboot_") && strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// bluefinSectionURL names section ext (".linux", ".initrd", ".ucode") of
// release version's netboot UKI below mac's directory.
func bluefinSectionURL(mac, version, ext string) string {
	return bluefinDirURL(mac) + "/" + strings.TrimSuffix(versions.BluefinNetbootUKI(version), ".efi") + ext
}

// bluefinSectionRelease resolves bluefin-server-netboot_<version>.<ext> to
// the cached release and the section it names.
func bluefinSectionRelease(name string) (versions.BluefinManifest, string, bool) {
	for ext, section := range bluefinSections {
		base, ok := strings.CutSuffix(name, ext)
		if !ok {
			continue
		}
		if m, ok := bluefinManifestWhere(func(m versions.BluefinManifest) bool { return m.NetbootUKI == base+".efi" }); ok {
			return m, section, true
		}
	}
	return versions.BluefinManifest{}, "", false
}

// serveBluefinUKISection serves one section payload of a release's netboot
// UKI straight out of the UKI file (HEAD and Range work). The .linux fetch
// starts a BIOS netboot and is recorded like the UKI fetch; the initrds
// record nothing.
func serveBluefinUKISection(w http.ResponseWriter, r *http.Request, mac, name string) {
	m, section, ok := bluefinSectionRelease(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not a section of a cached Bluefin release")
		return
	}
	uki, info, err := cachedUKI(m)
	if err != nil {
		slog.Error("Bluefin netboot UKI unreadable", "version", m.Version, "error", err)
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	payload, ok := uki.sections[section]
	if !ok {
		writeError(w, http.StatusNotFound, "the netboot UKI has no "+section+" section")
		return
	}
	if section == ".linux" {
		host, now, ok := bluefinBootHost(w, r, mac, hardware.PlatformPCBIOS)
		if !ok {
			return
		}
		recordBluefinNetboot(r, mac, host, hardware.PlatformPCBIOS, m.Version, name, now)
	} else if host, ok := hardware.Get(mac); !ok || host.OS != "bluefin" {
		writeError(w, http.StatusNotFound, "host not registered as bluefin")
		return
	}
	path := bluefinUKIPath(m)
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer config.CloseQuietly(f, path)
	tag := m.SHA256Sums[m.NetbootUKI]
	if tag == "" {
		tag = fmt.Sprintf("%x-%x", info.Size(), info.ModTime().UnixNano())
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+tag+strings.ReplaceAll(section, ".", "-")+`"`)
	http.ServeContent(w, r, name, info.ModTime(), io.NewSectionReader(f, payload.offset, payload.size))
}
