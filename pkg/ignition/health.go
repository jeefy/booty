package ignition

// HealthUnit is booty-health.service: once multi-user.target is reached it
// runs the health-report script at scriptPath and retries every 30 s
// until the POST succeeds. DefaultDependencies=no keeps multi-user.target
// from ordering itself after the unit (a target implicitly does that for
// everything it Wants), which would make After=multi-user.target a cycle.
func HealthUnit(scriptPath string) string {
	return `[Unit]
Description=Report this host's health (failed units, journal errors, hardware) to Booty
DefaultDependencies=no
Conflicts=shutdown.target
Before=shutdown.target
After=multi-user.target network-online.target ` + BootedUnitName + `
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
Restart=on-failure
RestartSec=30s
ExecStart=` + scriptPath + `

[Install]
WantedBy=multi-user.target
`
}

// HealthReportScript is written to HealthReportScriptPath (or Bluefin's
// /etc/booty) and POSTs the node's health to /health. It is plain bash
// with curl, systemctl, journalctl and uname; JSON is assembled by hand,
// so jsonEscape covers every string. The BOOTY_* variables exist for the
// test, which runs the script with stubs and a fake os-release.
// product_uuid is root-only in sysfs; read_file yields "" when it cannot
// be read and the report stays valid.
func HealthReportScript(server string) string {
	return `#!/bin/bash
set -u
export LC_ALL=C
SYS=${BOOTY_SYS:-/sys}
PROC=${BOOTY_PROC:-/proc}
. "${BOOTY_OS_RELEASE:-/etc/os-release}"
if [ -n "${BOOTY_MAC:-}" ]; then MAC=$BOOTY_MAC; else
  set -- $(ip -o route get 1); while [ $# -gt 1 ] && [ "$1" != dev ]; do shift; done; MAC=$(cat "$SYS/class/net/$2/address")
fi

json_escape() {
  local s=$1 out="" i c
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/\\r}
  s=${s//$'\t'/\\t}
  if [[ $s == *[[:cntrl:]]* ]]; then
    for ((i = 0; i < ${#s}; i++)); do
      c=${s:i:1}
      if [[ $c < ' ' ]]; then printf -v c '\\u%04x' "'$c"; fi
      out+=$c
    done
    s=$out
  fi
  printf '%s' "$s"
}
json_string() { printf '"%s"' "$(json_escape "$1")"; }
json_array() {
  local sep="" item
  printf '['
  for item in "$@"; do printf '%s%s' "$sep" "$(json_string "$item")"; sep=,; done
  printf ']'
}
read_file() { tr -d '\n' < "$1" 2>/dev/null || true; }

RUNNING=${OSTREE_VERSION:-${VERSION_ID:-${VERSION:-}}}
FAILED=()
while read -r unit _; do [ -n "$unit" ] && FAILED+=("$unit"); done < <(systemctl list-units --state=failed --plain --no-legend 2>/dev/null)
ALL=()
while IFS= read -r line; do ALL+=("${line:0:300}"); done < <(journalctl -p err -b -o short-iso --no-hostname --no-pager -n 50 2>/dev/null)
ERRORS=()
total=0
for ((i = ${#ALL[@]} - 1; i >= 0; i--)); do
  line=${ALL[i]}
  (( total + ${#line} > 8192 )) && break
  total=$(( total + ${#line} ))
  ERRORS=("$line" "${ERRORS[@]}")
done
FIRMWARE=bios; [ -d "$SYS/firmware/efi" ] && FIRMWARE=uefi

BODY=$(printf '{"running":%s,"failedUnits":%s,"journalErrors":%s,"dmi":{"vendor":%s,"product":%s,"biosVersion":%s,"productUUID":%s},"firmware":%s,"kernel":%s,"bootID":%s}' \
  "$(json_string "$RUNNING")" \
  "$(json_array "${FAILED[@]+"${FAILED[@]}"}")" \
  "$(json_array "${ERRORS[@]+"${ERRORS[@]}"}")" \
  "$(json_string "$(read_file "$SYS/class/dmi/id/sys_vendor")")" \
  "$(json_string "$(read_file "$SYS/class/dmi/id/product_name")")" \
  "$(json_string "$(read_file "$SYS/class/dmi/id/bios_version")")" \
  "$(json_string "$(read_file "$SYS/class/dmi/id/product_uuid")")" \
  "$(json_string "$FIRMWARE")" \
  "$(json_string "$(uname -r)")" \
  "$(json_string "$(read_file "$PROC/sys/kernel/random/boot_id")")")
printf '%s' "$BODY" | curl -fsS --max-time 20 --retry 2 --retry-connrefused -X POST -H 'Content-Type: application/json' --data-binary @- "http://` + server + `/health?mac=$MAC" >/dev/null || { echo "booty unreachable or refused the health report" >&2; exit 1; }
echo "health reported: running=$RUNNING failedUnits=${#FAILED[@]} journalErrors=${#ERRORS[@]}"
`
}
