package report

import (
	"regexp"
	"strings"
)

// Placeholders the redactor substitutes. They are what the tests grep
// for the absence of the originals against.
const (
	RedactedIP    = "<ip>"
	RedactedMAC   = "<mac>"
	RedactedHost  = "<host>"
	RedactedUUID  = "<uuid>"
	RedactedKey   = "<key>"
	RedactedToken = "<token>"
)

var (
	// bluefinMACPath is the per-host directory Booty serves a Bluefin host
	// from; the MAC in it is written with dashes, colons or %3A.
	bluefinMACPath = regexp.MustCompile(`(?i)/bluefin/(?:[0-9a-f]{2}(?::|-|%3a)){5}[0-9a-f]{2}/`)
	uuidPattern    = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	macPattern     = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}(?::|-|%3a)){5}[0-9a-f]{2}\b`)
	// keyPattern is the base64 body of an SSH public key (every OpenSSH
	// key blob starts with AAAA), or any comparably long base64 run
	// after it.
	keyPattern = regexp.MustCompile(`AAAA[0-9A-Za-z+/]{40,}={0,2}`)
	// GitHub tokens have recognisable prefixes; other bearer/token= values
	// are caught by shape.
	ghTokenPattern = regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`)
	bearerPattern  = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	tokenKVPattern = regexp.MustCompile(`(?i)\b([a-z_-]*(?:token|password|passwd|secret|api[_-]?key))(=|:\s*|"\s*:\s*")([^\s&"',;]+)`)
	// tokenFlagPattern is kubeadm's `--token abcdef.0123456789abcdef` (and
	// k0s's --token-file value): a bootstrap token is a credential.
	tokenFlagPattern = regexp.MustCompile(`(?i)(--[a-z-]*token(?:-file)?)(\s+|=)([^\s"']+)`)
	// ipv6Pattern over-matches (times such as 10:00:01 have the same
	// shape), so plausibleIPv6 filters what it finds: a compressed ::
	// form or the full eight groups. A trailing IPv4 (::ffff:1.2.3.4) and
	// a %zone are part of the address; brackets are dropped with it.
	ipv6Pattern = regexp.MustCompile(`(?i)\[?(?:[0-9a-f]{0,4}:){2,7}(?:(?:\d{1,3}\.){3}\d{1,3}|[0-9a-f]{0,4})(?:%[0-9a-z._-]+)?\]?`)
	ipv4Pattern = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b(?:/\d{1,2}\b)?`)
)

// Redact removes what could identify a machine or a network from s:
// MACs (colon, dash or %3A separated) become <mac>, the /bluefin/<mac>/
// directory in URLs /bluefin/<host>/, IPv4 and IPv6 addresses (zoned,
// bracketed, IPv4-mapped) <ip>, UUIDs (the DMI product UUID, boot IDs)
// <uuid>, SSH key blobs <key>, GitHub tokens and bearer/token= values
// <token>, and every token equal to one of names (registered hostnames,
// node names; case-insensitive, whole word) <host>. Everything else is
// kept verbatim.
func Redact(s string, names ...string) string {
	s = bluefinMACPath.ReplaceAllString(s, "/bluefin/"+RedactedHost+"/")
	s = uuidPattern.ReplaceAllString(s, RedactedUUID)
	s = macPattern.ReplaceAllString(s, RedactedMAC)
	s = keyPattern.ReplaceAllString(s, RedactedKey)
	s = ghTokenPattern.ReplaceAllString(s, RedactedToken)
	s = bearerPattern.ReplaceAllString(s, "$1 "+RedactedToken)
	s = tokenKVPattern.ReplaceAllString(s, "$1$2"+RedactedToken)
	s = tokenFlagPattern.ReplaceAllString(s, "$1$2"+RedactedToken)
	s = ipv6Pattern.ReplaceAllStringFunc(s, func(m string) string {
		if plausibleIPv6(m) {
			return RedactedIP
		}
		return m
	})
	s = ipv4Pattern.ReplaceAllString(s, RedactedIP)
	for _, name := range names {
		s = redactName(s, name)
	}
	return s
}

// RedactAll redacts every line.
func RedactAll(lines []string, names ...string) []string {
	if lines == nil {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = Redact(l, names...)
	}
	return out
}

func plausibleIPv6(m string) bool {
	m = strings.Trim(m, "[]")
	if i := strings.IndexByte(m, '%'); i >= 0 {
		m = m[:i]
	}
	if strings.Contains(m, "::") {
		// "::" alone, or a lone ":" pair inside prose, is not an address.
		return len(m) >= 3 && strings.Count(m, ":") <= 7
	}
	return strings.Count(m, ":") == 7
}

// redactName replaces whole-word, case-insensitive occurrences of name.
// Hostname characters are letters, digits and hyphens, so anything else
// (including '.') delimits a word, and node-1.lan becomes <host>.lan.
func redactName(s, name string) string {
	name = strings.TrimSpace(name)
	if len(name) < 2 {
		return s
	}
	re, err := regexp.Compile(`(?i)(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9-]|$)`)
	if err != nil {
		return s
	}
	// Two passes: a match consumes its delimiters, so back-to-back
	// occurrences ("aren aren") need the second one.
	for range 2 {
		s = re.ReplaceAllString(s, "$1"+RedactedHost+"$2")
	}
	return s
}
