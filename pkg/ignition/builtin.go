// Package ignition generates the Ignition fragment Booty merges into every
// registered host's config (hostname, update timer, booted callback, SSH
// keys) so users no longer hand-write that boilerplate in their Butane.
package ignition

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/jeefy/booty/pkg/config"
)

const (
	FeatureHostname = "hostname"
	FeatureUpdate   = "update"
	FeatureBooted   = "booted"
	FeatureSSHKeys  = "sshkeys"
	FeatureNone     = "none"

	UpdateCheckScriptPath = "/usr/local/bin/booty-update-check"
)

var knownFeatures = map[string]bool{
	FeatureHostname: true,
	FeatureUpdate:   true,
	FeatureBooted:   true,
	FeatureSSHKeys:  true,
}

// Features is the set of enabled builtin fragments.
type Features map[string]bool

// ParseFeatures parses the --builtin value: a comma separated list of
// feature names, or "none" to disable the builtin fragment entirely.
func ParseFeatures(s string) (Features, error) {
	f := Features{}
	for _, raw := range strings.Split(s, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case name == "":
			continue
		case name == FeatureNone:
			return Features{}, nil
		case !knownFeatures[name]:
			return nil, fmt.Errorf("invalid --%s entry %q: must be %s or %s", config.Builtin, raw, knownList(), FeatureNone)
		}
		f[name] = true
	}
	return f, nil
}

func knownList() string {
	names := make([]string, 0, len(knownFeatures))
	for n := range knownFeatures {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Enabled reports whether any fragment is active; false means --builtin=none.
func (f Features) Enabled() bool { return len(f) > 0 }

// Input is everything the fragment depends on. Server is the host[:port]
// clients use to reach Booty.
type Input struct {
	Hostname string
	Server   string
	SSHKeys  []string
}

// Fragment builds Booty's Ignition config for a host. It is always a valid
// spec 3.4.0 config, possibly with no content when every feature is off or
// has nothing to contribute.
func Fragment(in Input, f Features) types.Config {
	cfg := types.Config{}
	cfg.Ignition.Version = types.MaxVersion.String()

	if f[FeatureHostname] && in.Hostname != "" {
		cfg.Storage.Files = append(cfg.Storage.Files, inlineFile("/etc/hostname", in.Hostname+"\n", 0o644))
	}
	if f[FeatureSSHKeys] && len(in.SSHKeys) > 0 {
		user := types.PasswdUser{Name: "core"}
		for _, k := range in.SSHKeys {
			user.SSHAuthorizedKeys = append(user.SSHAuthorizedKeys, types.SSHAuthorizedKey(k))
		}
		cfg.Passwd.Users = append(cfg.Passwd.Users, user)
	}
	if f[FeatureBooted] {
		cfg.Systemd.Units = append(cfg.Systemd.Units, unit("booty-booted.service", true, bootedUnit(in.Server)))
	}
	if f[FeatureUpdate] {
		cfg.Storage.Files = append(cfg.Storage.Files, inlineFile(UpdateCheckScriptPath, UpdateCheckScript(in.Server), 0o755))
		cfg.Systemd.Units = append(cfg.Systemd.Units,
			unit("booty-update.service", false, updateService),
			unit("booty-update.timer", true, updateTimer),
		)
	}
	return cfg
}

func inlineFile(path, contents string, mode int) types.File {
	src := dataURL(contents)
	return types.File{
		Node:          types.Node{Path: path},
		FileEmbedded1: types.FileEmbedded1{Contents: types.Resource{Source: &src}, Mode: &mode},
	}
}

func dataURL(s string) string {
	if len(s) < 128 {
		return "data:," + url.PathEscape(s)
	}
	return "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(s))
}

func unit(name string, enabled bool, contents string) types.Unit {
	u := types.Unit{Name: name, Contents: &contents}
	if enabled {
		u.Enabled = &enabled
	}
	return u
}

// bootedUnit calls POST /booted once the installed system is up. systemd
// expands $VAR in ExecStart itself, hence $$ for everything meant for bash.
func bootedUnit(server string) string {
	return `[Unit]
Description=Tell Booty this host finished installing (clears doInstall)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash -c 'set -- $$(ip -o route get 1); while [ $$# -gt 1 ] && [ "$$1" != dev ]; do shift; done; MAC=$$(cat /sys/class/net/$$2/address); curl -fsS --retry 5 --retry-connrefused -X POST "http://` + server + `/booted?mac=$$MAC"'

[Install]
WantedBy=multi-user.target
`
}

const updateService = `[Unit]
Description=Ask Booty whether this host needs a reboot to pick up an update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=` + UpdateCheckScriptPath + `
`

const updateTimer = `[Unit]
Description=Run the Booty update check every 10 minutes

[Timer]
OnCalendar=*:0/10
RandomizedDelaySec=60
Persistent=false

[Install]
WantedBy=timers.target
`

// UpdateCheckScript is written to UpdateCheckScriptPath on the host. It is a
// plain file (not an ExecStart line), so shell variables use a single $.
// It fails closed: when Booty is unreachable or answers something
// unexpected the reboot flag is left as it is.
func UpdateCheckScript(server string) string {
	return `#!/bin/bash
set -u
. /etc/os-release
set -- $(ip -o route get 1); while [ $# -gt 1 ] && [ "$1" != dev ]; do shift; done; MAC=$(cat /sys/class/net/$2/address)
IMAGE=""; DIGEST=""
if [ "${ID:-}" = coreos ] || [ -n "${OSTREE_VERSION:-}" ]; then
  if command -v rpm-ostree >/dev/null && command -v jq >/dev/null; then
    IMAGE=$(rpm-ostree status -b --json | jq -r '.deployments[0]."container-image-reference" // empty')
    DIGEST=$(rpm-ostree status -b --json | jq -r '.deployments[0]."container-image-reference-digest" // empty')
  fi
fi
RESP=$(curl -fsS --max-time 15 -G "http://` + server + `/update-check" --data-urlencode "mac=$MAC" --data-urlencode "os=${ID:-}" --data-urlencode "version=${OSTREE_VERSION:-${VERSION_ID:-${VERSION:-}}}" --data-urlencode "image=$IMAGE" --data-urlencode "digest=$DIGEST") || { echo "booty unreachable; leaving reboot state alone"; exit 0; }
case "$RESP" in
  *'"rebootRequired":true'*)  echo "reboot required: $RESP"; touch /var/run/reboot-required ;;
  *'"rebootRequired":false'*) echo "up to date: $RESP"; rm -f /var/run/reboot-required ;;
  *) echo "unexpected response, leaving state alone: $RESP" ;;
esac
`
}

// LoadSSHKeys combines the keys from file (one per line, blank lines and
// #-comments ignored) with the inline keys. A missing or unreadable file is
// an error only when file is non-empty.
func LoadSSHKeys(file string, inline []string) ([]string, error) {
	var keys []string
	for _, k := range inline {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if file == "" {
		return keys, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return keys, err
	}
	defer config.CloseQuietly(f, file)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keys = append(keys, line)
	}
	if err := sc.Err(); err != nil {
		return keys, err
	}
	return keys, nil
}
