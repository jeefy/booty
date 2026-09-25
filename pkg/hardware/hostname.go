package hardware

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

// MaxHostnameLen is the RFC 1123 limit for a fully qualified name.
const MaxHostnameLen = 253

// ErrInvalidHostname is returned for names that are not RFC 1123 labels or
// dotted names.
var ErrInvalidHostname = errors.New("invalid hostname")

var hostnameRe = regexp.MustCompile(`^(?i)[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// ValidateHostname accepts an RFC 1123 label or dotted name (letters, digits
// and hyphens, no leading/trailing hyphen per label) of at most
// MaxHostnameLen characters. The empty string is rejected; callers that
// treat it as "unset" must check for it first.
func ValidateHostname(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidHostname)
	}
	if len(name) > MaxHostnameLen {
		return fmt.Errorf("%w: %d characters exceeds %d", ErrInvalidHostname, len(name), MaxHostnameLen)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 {
			return fmt.Errorf("%w: label %q exceeds 63 characters", ErrInvalidHostname, label)
		}
	}
	if !hostnameRe.MatchString(name) {
		return fmt.Errorf("%w: %q must be an RFC 1123 label or dotted name", ErrInvalidHostname, name)
	}
	return nil
}

// HostnameVars are the fields available to a hostname template.
type HostnameVars struct {
	// MAC is the canonical colon-separated MAC (aa:bb:cc:dd:ee:ff).
	MAC string
	// MACSuffix is the last three bytes as hex without separators (ddeeff).
	MACSuffix string
	// MACFlat is all six bytes as hex without separators (aabbccddeeff).
	MACFlat string
	// IP is the client's address as seen by Booty; may be empty.
	IP string
}

// NewHostnameVars derives the template fields from a MAC that has already
// been normalized with NormalizeMAC.
func NewHostnameVars(mac, ip string) HostnameVars {
	flat := strings.ReplaceAll(mac, ":", "")
	suffix := flat
	if len(flat) >= 6 {
		suffix = flat[len(flat)-6:]
	}
	return HostnameVars{MAC: mac, MACSuffix: suffix, MACFlat: flat, IP: ip}
}

// HostnameTemplate renders hostnames for auto-registered hosts.
type HostnameTemplate struct {
	tmpl *template.Template
}

// dummy values used to prove a template renders a valid hostname at startup.
const (
	validationMAC = "02:00:00:aa:bb:cc"
	validationIP  = "192.0.2.1"
)

// ParseHostnameTemplate parses src as a Go text/template over HostnameVars
// and checks that it renders a valid hostname for a dummy MAC, so a broken
// template is rejected at startup rather than on the first boot.
func ParseHostnameTemplate(src string) (*HostnameTemplate, error) {
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("hostname template is empty")
	}
	tmpl, err := template.New("hostname").Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parsing hostname template %q: %w", src, err)
	}
	ht := &HostnameTemplate{tmpl: tmpl}
	if _, err := ht.Render(validationMAC, validationIP); err != nil {
		return nil, fmt.Errorf("hostname template %q: %w", src, err)
	}
	return ht, nil
}

// Render executes the template for mac (any parseable form) and ip and
// validates the result with ValidateHostname.
func (t *HostnameTemplate) Render(mac, ip string) (string, error) {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.tmpl.Execute(&buf, NewHostnameVars(mac, ip)); err != nil {
		return "", fmt.Errorf("rendering: %w", err)
	}
	name := strings.TrimSpace(buf.String())
	if err := ValidateHostname(name); err != nil {
		return "", fmt.Errorf("rendered %q: %w", name, err)
	}
	return name, nil
}

// ValidateAutoRegisterOS accepts the empty string (auto-registration off) or
// one of the operating systems /register accepts.
func ValidateAutoRegisterOS(os string) error {
	if os == "" || IsValidOS(os) {
		return nil
	}
	return fmt.Errorf("invalid auto-register os %q: must be one of %s", os, ValidOSList())
}
