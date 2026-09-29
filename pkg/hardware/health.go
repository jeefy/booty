package hardware

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Health is what booty-health.service POSTs to /health once the node
// reached multi-user.target: the release it runs, its failed units, an
// excerpt of the boot's error-level journal, and the machine it runs on.
// ReceivedAt and the caps below are Booty's; everything else is the node's
// word. The autopilot's health gate (plan P3) reads it; P1 only records it.
type Health struct {
	ReceivedAt string `json:"receivedAt"`
	// BootID is /proc/sys/kernel/random/boot_id, so a report can be told
	// from the previous boot's and a retried POST from a new boot.
	BootID        string   `json:"bootID"`
	Running       string   `json:"running"`
	FailedUnits   []string `json:"failedUnits"`
	JournalErrors []string `json:"journalErrors"`
	DMI           DMI      `json:"dmi"`
	// Firmware is FirmwareUEFI or FirmwareBIOS (or "" when unknown).
	Firmware string `json:"firmware"`
	Kernel   string `json:"kernel"`
}

// DMI identifies the hardware from /sys/class/dmi/id. Vendor, Product and
// BIOSVersion describe the model and may go into a report. ProductUUID is
// the machine's identity (/sys/class/dmi/id/product_uuid, root-readable,
// what the kubelet publishes as nodeInfo.systemUUID): the autopilot uses
// it to confirm hostname-to-node matches and it must stay out of every
// report, like the MAC.
type DMI struct {
	Vendor      string `json:"vendor"`
	Product     string `json:"product"`
	BIOSVersion string `json:"biosVersion"`
	ProductUUID string `json:"productUUID,omitempty"`
}

// Firmware values a health report may carry.
const (
	FirmwareUEFI = "uefi"
	FirmwareBIOS = "bios"
)

// Caps on a health report, applied server-side whatever the node sent.
const (
	MaxHealthFailedUnits   = 200
	MaxHealthJournalLines  = 50
	MaxHealthJournalLine   = 300
	MaxHealthJournalBytes  = 8 << 10
	MaxHealthFieldLen      = 256
	MaxHealthUnitNameLen   = 256
	maxHealthFieldTruncMsg = "…"
)

var ErrInvalidHealth = errors.New("invalid health report")

// Normalize trims and caps every field of a report so that whatever a node
// sends, what Booty stores is bounded. It rejects a firmware value it
// does not know.
func (h *Health) Normalize() error {
	h.BootID = clip(strings.TrimSpace(h.BootID), MaxHealthFieldLen)
	h.Running = clip(strings.TrimSpace(h.Running), MaxHealthFieldLen)
	h.Kernel = clip(strings.TrimSpace(h.Kernel), MaxHealthFieldLen)
	h.DMI.Vendor = clip(strings.TrimSpace(h.DMI.Vendor), MaxHealthFieldLen)
	h.DMI.Product = clip(strings.TrimSpace(h.DMI.Product), MaxHealthFieldLen)
	h.DMI.BIOSVersion = clip(strings.TrimSpace(h.DMI.BIOSVersion), MaxHealthFieldLen)
	h.DMI.ProductUUID = strings.ToLower(clip(strings.TrimSpace(h.DMI.ProductUUID), MaxHealthFieldLen))
	h.Firmware = strings.ToLower(strings.TrimSpace(h.Firmware))
	switch h.Firmware {
	case "", FirmwareUEFI, FirmwareBIOS:
	default:
		return fmt.Errorf("%w: firmware %q must be %q or %q", ErrInvalidHealth, h.Firmware, FirmwareUEFI, FirmwareBIOS)
	}
	units := make([]string, 0, len(h.FailedUnits))
	for _, u := range h.FailedUnits {
		if u = strings.TrimSpace(u); u != "" {
			units = append(units, clip(u, MaxHealthUnitNameLen))
		}
		if len(units) == MaxHealthFailedUnits {
			break
		}
	}
	h.FailedUnits = units
	h.JournalErrors = capJournal(h.JournalErrors)
	return nil
}

// capJournal keeps the newest MaxHealthJournalLines lines, each clipped to
// MaxHealthJournalLine bytes, within MaxHealthJournalBytes in total; the
// node applies the same limits, so a well-behaved report passes unchanged.
func capJournal(lines []string) []string {
	if len(lines) > MaxHealthJournalLines {
		lines = lines[len(lines)-MaxHealthJournalLines:]
	}
	out := make([]string, 0, len(lines))
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		l := clip(strings.TrimRight(lines[i], "\r\n"), MaxHealthJournalLine)
		if total+len(l) > MaxHealthJournalBytes {
			break
		}
		total += len(l)
		out = append(out, l)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len(maxHealthFieldTruncMsg)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + maxHealthFieldTruncMsg
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// SameBoot reports whether other is a report from the same boot as h.
func (h *Health) SameBoot(other *Health) bool {
	return h != nil && other != nil && h.BootID != "" && h.BootID == other.BootID
}

// targetVersionPattern bounds what a release version may look like; it
// ends up in file paths and URLs (see versions.ValidBluefinVersion).
var targetVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z._+~-]{0,63}$`)

var ErrInvalidTargetVersion = errors.New("invalid targetVersion")

// ValidateTargetVersion accepts an empty value or a plain release version
// such as 4757.2.0, 44.20260913.2.1 or 26.09.673. Whether the release is
// actually cached is the server's business (pkg/versions).
func ValidateTargetVersion(v string) error {
	if v == "" {
		return nil
	}
	if !targetVersionPattern.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("%w %q: must be a release version such as 4757.2.0", ErrInvalidTargetVersion, v)
	}
	return nil
}
