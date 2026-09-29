// Package report renders the autopilot's report for a release it blamed
// (plan P4): a redacted Markdown document plus a JSON twin written to
// data/autopilot/reports/<os>-<version>.{md,json}, and the GitHub poster
// that files Bluefin reports as issues, deduplicated by a hidden marker.
// Nothing that identifies a machine or a network (hostname, MAC, IP,
// product UUID) leaves this package; the tests grep for it.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
)

// Boot paths a report names.
const (
	BootPathBIOSDiskless = "bios-diskless"
	BootPathUEFIHTTP     = "uefi-http"
	BootPathUEFIPXE      = "uefi-pxe"
	BootPathBIOSPXE      = "bios-pxe"
	BootPathUnknown      = "unknown"
)

// Attempt is one reboot into a target as the report shows it.
type Attempt struct {
	Attempt     int       `json:"attempt"`
	Target      string    `json:"target"`
	Outcome     string    `json:"outcome"`
	Class       string    `json:"class,omitempty"`
	Note        string    `json:"note,omitempty"`
	T0          time.Time `json:"t0,omitzero"`
	Ended       time.Time `json:"ended,omitzero"`
	FailedUnits []string  `json:"failedUnits,omitempty"`
	Timeline    []string  `json:"timeline,omitempty"`
}

// Hardware is the DMI model description plus what the boot looked like.
type Hardware struct {
	Vendor      string `json:"vendor,omitempty"`
	Product     string `json:"product,omitempty"`
	BIOSVersion string `json:"biosVersion,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Kernel      string `json:"kernel,omitempty"`
	BootPath    string `json:"bootPath,omitempty"`
}

// Node is what the kubelet published about the node.
type Node struct {
	KubeletVersion          string `json:"kubeletVersion,omitempty"`
	OSImage                 string `json:"osImage,omitempty"`
	ContainerRuntimeVersion string `json:"containerRuntimeVersion,omitempty"`
	KernelVersion           string `json:"kernelVersion,omitempty"`
}

// CNI names the network plugin Booty installed, when it did.
type CNI struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// Input is what the controller hands the builder: the report stub it
// keeps in state.json. JournalErrors is the raw excerpt and Names the
// hostnames/node names to strip; neither appears in the output as-is.
type Input struct {
	OS             string
	Version        string
	LastGood       string
	Mode           string
	Class          string
	RollbackResult string
	DMIHash        string
	Draft          bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Attempts       []Attempt
	Hardware       Hardware
	Node           Node
	CNI            CNI
	JournalErrors  []string
	Names          []string
}

// Document is the JSON twin of the Markdown: the same redacted facts.
type Document struct {
	Marker         string    `json:"marker"`
	OS             string    `json:"os"`
	Version        string    `json:"version"`
	LastGood       string    `json:"lastGood,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Draft          bool      `json:"draft"`
	Class          string    `json:"class,omitempty"`
	AttemptCount   int       `json:"attemptCount"`
	RollbackResult string    `json:"rollbackResult,omitempty"`
	DMIHash        string    `json:"dmiHash,omitempty"`
	CreatedAt      time.Time `json:"createdAt,omitzero"`
	UpdatedAt      time.Time `json:"updatedAt,omitzero"`
	Hardware       Hardware  `json:"hardware"`
	Node           Node      `json:"node"`
	CNI            *CNI      `json:"cni,omitempty"`
	Attempts       []Attempt `json:"attempts"`
	FailedUnits    []string  `json:"failedUnits"`
	JournalErrors  []string  `json:"journalErrors"`
}

// neverReportedHealth says whether a failure class means the boot ended
// before booty-health.service could run.
func neverReportedHealth(class string) bool {
	switch class {
	case "boot-loop", "no-ignition", "hung", "timeout":
		return true
	}
	return false
}

// Report is a rendered report.
type Report struct {
	Markdown string
	JSON     any
}

// Document returns the JSON twin when it is one.
func (r Report) Document() *Document {
	d, _ := r.JSON.(*Document)
	return d
}

// Key is the report's name on disk and in State.Reports: <os>-<version>.
func Key(osName, version string) string { return osName + "-" + version }

// Marker is the hidden HTML comment that deduplicates issues: the same
// release from the same machine (by DMI hash) is reported once.
func Marker(osName, version, dmiHash string) string {
	return fmt.Sprintf("<!-- %s -->", MarkerText(osName, version, dmiHash))
}

// MarkerPrefix is the part of the marker that identifies the release
// alone; issues are searched by it.
func MarkerPrefix(osName, version string) string {
	return "booty-autopilot: " + osName + " " + version
}

// MarkerText is the marker without the comment delimiters.
func MarkerText(osName, version, dmiHash string) string {
	if dmiHash == "" {
		dmiHash = "unknown"
	}
	return MarkerPrefix(osName, version) + " " + dmiHash
}

// Build renders in. Every free-text field passes through Redact.
func Build(in Input) Report {
	names := in.Names
	red := func(s string) string { return Redact(s, names...) }
	doc := &Document{
		Marker:         Marker(in.OS, in.Version, in.DMIHash),
		OS:             in.OS,
		Version:        in.Version,
		LastGood:       in.LastGood,
		Mode:           in.Mode,
		Draft:          in.Draft,
		Class:          in.Class,
		AttemptCount:   len(in.Attempts),
		RollbackResult: red(in.RollbackResult),
		DMIHash:        in.DMIHash,
		CreatedAt:      in.CreatedAt,
		UpdatedAt:      in.UpdatedAt,
		Hardware: Hardware{
			Vendor:      red(in.Hardware.Vendor),
			Product:     red(in.Hardware.Product),
			BIOSVersion: red(in.Hardware.BIOSVersion),
			Firmware:    in.Hardware.Firmware,
			Kernel:      red(in.Hardware.Kernel),
			BootPath:    in.Hardware.BootPath,
		},
		Node: Node{
			KubeletVersion:          red(in.Node.KubeletVersion),
			OSImage:                 red(in.Node.OSImage),
			ContainerRuntimeVersion: red(in.Node.ContainerRuntimeVersion),
			KernelVersion:           red(in.Node.KernelVersion),
		},
		Attempts:      make([]Attempt, 0, len(in.Attempts)),
		FailedUnits:   []string{},
		JournalErrors: RedactAll(in.JournalErrors, names...),
	}
	if doc.Hardware.BootPath == "" {
		doc.Hardware.BootPath = BootPathUnknown
	}
	if doc.JournalErrors == nil {
		doc.JournalErrors = []string{}
	}
	if in.CNI.Name != "" {
		doc.CNI = &CNI{Name: red(in.CNI.Name), Version: red(in.CNI.Version)}
	}
	seenUnit := map[string]bool{}
	for _, a := range in.Attempts {
		a.Note = red(a.Note)
		a.Target = red(a.Target)
		a.FailedUnits = RedactAll(a.FailedUnits, names...)
		a.Timeline = RedactAll(a.Timeline, names...)
		doc.Attempts = append(doc.Attempts, a)
		for _, u := range a.FailedUnits {
			if !seenUnit[u] {
				seenUnit[u] = true
				doc.FailedUnits = append(doc.FailedUnits, u)
			}
		}
	}
	return Report{Markdown: renderMarkdown(doc), JSON: doc}
}

func renderMarkdown(d *Document) string {
	var b strings.Builder
	title := osTitle(d.OS)
	fmt.Fprintf(&b, "# %s %s: %s (autopilot report)\n\n", title, d.Version, orUnknown(d.Class))
	fmt.Fprintf(&b, "%s\n\n", d.Marker)
	status := "quarantined"
	if d.Draft {
		status = "draft (release in TIMEOUT, retry pending)"
	}
	fmt.Fprintf(&b, "Booty's autopilot (`--autopilot=%s`) rolled this release out, watched it fail the health gate %s and rolled the host back. Status: **%s**.\n\n", orUnknown(d.Mode), plural(d.AttemptCount, "times"), status)

	b.WriteString("## Release\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n| OS | %s |\n| Release | `%s` |\n| lastGood | `%s` |\n| Attempts | %d |\n| Last failure class | `%s` |\n| Rollback | %s |\n\n",
		title, d.Version, orDash(d.LastGood), d.AttemptCount, orUnknown(d.Class), orDash(d.RollbackResult))

	b.WriteString("## Machine\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Boot path | `%s` |\n| Hardware | %s |\n| BIOS | %s |\n| Firmware | %s |\n| Kernel | %s |\n",
		d.Hardware.BootPath, orDash(strings.TrimSpace(d.Hardware.Vendor+" "+d.Hardware.Product)), orDash(d.Hardware.BIOSVersion), orDash(d.Hardware.Firmware), orDash(d.Hardware.Kernel))
	if d.Node.KubeletVersion != "" || d.Node.OSImage != "" || d.Node.ContainerRuntimeVersion != "" {
		fmt.Fprintf(&b, "| kubelet | %s |\n| Node osImage | %s |\n| Container runtime | %s |\n", orDash(d.Node.KubeletVersion), orDash(d.Node.OSImage), orDash(d.Node.ContainerRuntimeVersion))
		if d.Node.KernelVersion != "" && d.Node.KernelVersion != d.Hardware.Kernel {
			fmt.Fprintf(&b, "| Node kernel | %s |\n", d.Node.KernelVersion)
		}
	}
	if d.CNI != nil {
		fmt.Fprintf(&b, "| CNI | %s %s |\n", d.CNI.Name, d.CNI.Version)
	}
	fmt.Fprintf(&b, "| Machine id | dmi-hash `%s` (sha256 of the DMI product UUID, first 8 bytes) |\n\n", orUnknown(d.DMIHash))

	b.WriteString("## Attempts\n\n")
	if len(d.Attempts) == 0 {
		b.WriteString("_none recorded_\n\n")
	}
	for _, a := range d.Attempts {
		outcome := a.Outcome
		if a.Class != "" {
			outcome += " (`" + a.Class + "`)"
		}
		fmt.Fprintf(&b, "### Attempt %d into `%s`: %s\n\n", a.Attempt, a.Target, outcome)
		if a.Note != "" {
			fmt.Fprintf(&b, "%s\n\n", a.Note)
		}
		if len(a.FailedUnits) > 0 {
			b.WriteString("Failed units:\n\n")
			for _, u := range a.FailedUnits {
				fmt.Fprintf(&b, "- `%s`\n", u)
			}
			b.WriteString("\n")
		}
		if len(a.Timeline) > 0 {
			b.WriteString("Timeline (relative to the kernel/UKI fetch):\n\n```\n")
			for _, l := range a.Timeline {
				b.WriteString(l)
				b.WriteString("\n")
			}
			b.WriteString("```\n\n")
		}
	}

	b.WriteString("## Journal errors (redacted excerpt)\n\n")
	switch {
	case len(d.JournalErrors) == 0 && neverReportedHealth(d.Class):
		fmt.Fprintf(&b, "_The failing boot never got as far as a health report (`%s`), so there is no journal excerpt from it._\n\n", d.Class)
	case len(d.JournalErrors) == 0:
		b.WriteString("_no error-level journal lines were reported_\n\n")
	default:
		b.WriteString("Error-level lines from the failing boot's journal (`journalctl -p err -b --no-hostname`). Hostnames, addresses, MACs, UUIDs and keys are replaced by `<host>`, `<ip>`, `<mac>`, `<uuid>`, `<key>`.\n\n```\n")
		for _, l := range d.JournalErrors {
			b.WriteString(l)
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	}

	b.WriteString("---\n")
	fmt.Fprintf(&b, "_Filed by [Booty](https://github.com/jeefy/booty)'s autopilot. Created %s, updated %s. No hostnames, MAC or IP addresses are included._\n", stamp(d.CreatedAt), stamp(d.UpdatedAt))
	return b.String()
}

func osTitle(osName string) string {
	switch osName {
	case "bluefin":
		return "Bluefin Server"
	case "flatcar":
		return "Flatcar"
	case "coreos":
		return "Fedora CoreOS"
	}
	return osName
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func plural(n int, many string) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d %s", n, many)
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

// keyNamePattern bounds a report file name: <os>-<version>, the same
// characters a targetVersion may have.
var keyNamePattern = regexp.MustCompile(`^[a-z]+-[0-9][0-9A-Za-z._+~-]{0,63}$`)

// ValidKey reports whether key is a well-formed report key.
func ValidKey(key string) bool {
	return keyNamePattern.MatchString(key) && !strings.Contains(key, "..")
}

// Paths are the two files a report is written to.
func Paths(dir, key string) (markdown, jsonPath string) {
	return filepath.Join(dir, key+".md"), filepath.Join(dir, key+".json")
}

// Write stores r under dir as <key>.md and <key>.json, each atomically.
func Write(dir, key string, r Report) error {
	if !ValidKey(key) {
		return fmt.Errorf("report: invalid key %q", key)
	}
	md, js := Paths(dir, key)
	data, err := json.MarshalIndent(r.JSON, "", "  ")
	if err != nil {
		return fmt.Errorf("report %s: encoding: %w", key, err)
	}
	if err := config.WriteFileAtomic(md, []byte(r.Markdown), 0o644); err != nil {
		return fmt.Errorf("report %s: %w", key, err)
	}
	if err := config.WriteFileAtomic(js, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("report %s: %w", key, err)
	}
	return nil
}

// Exists reports whether both files of key are on disk under dir.
func Exists(dir, key string) bool {
	md, js := Paths(dir, key)
	for _, p := range []string{md, js} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}
