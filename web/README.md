# Booty web UI

Vue 3 + TypeScript + Vite single-page app served by the Go binary under `/ui/`
(the server does `http.FileServer(http.Dir("./web/dist"))`, so `npm run build`
output must exist before starting `booty`).

## Layout

```
src/
  api.ts          fetch helpers (apiGet / apiGetText / apiPost / apiPut), ApiError from the JSON error envelope,
                  and normalised endpoint getters (getBootyData, getInfo, getAutopilotStatus, getPowerStatus,
                  getStorage, getClusterInfo, getEffectiveConfig)
  types/          wire types mirroring the Go API + normalisers, one module per domain
                  (host, power, autopilot, info, storage, config, cluster); import from `@/types`
  composables/    usePolling (visibility-aware poll with error backoff)
  utils/time.ts   relative / absolute time formatting for RFC3339 timestamps
  utils/fleet.ts  fleet helpers: running-label splitting, host status, pending list, preview URLs
  components/     ErrorAlert, LoadingState, EmptyState, HostForm
  views/          HomeView (status + Flatcar pin), HostsView, StorageView, AutopilotView, ConfigView, AboutView
  router/         hash-based routes (/, /hosts, /storage, /autopilot, /config, /about)
  assets/bootstrap.scss  the Bootstrap 5 Sass partials the UI uses
  assets/main.css design tokens (CSS variables) layered on top of Bootstrap 5
```

`src/assets/bootstrap.scss` compiles only the Bootstrap partials the templates
use (layout, reboot/type, grid, tables, forms, buttons, transitions, nav,
navbar, badge, alert, spinners, helpers, utilities). A component outside that
list (card, modal, dropdown, progress, …) renders unstyled until its partial
is added there. This cut the main CSS bundle from 238 kB (32.6 kB gzip) to
163 kB (23.8 kB gzip). No Bootstrap JS is used (the navbar collapse is driven
by Vue state).

### API responses

Views fetch through the `get*` helpers in `api.ts`, which run each body
through its normaliser in `@/types`. A body that is not a JSON object, or is
not JSON at all, becomes an `ApiError` ("Unexpected response from /info: …")
that the page's error alert shows; missing or mistyped fields fall back to
defaults inside the normalisers. The bare `apiGet<T>` is for endpoints
without a normaliser.

### Polling

Home and Autopilot refresh every 30 s via `usePolling`. It pauses while the
tab is hidden, refreshes as soon as it is visible again, and after a failed
poll doubles the delay (up to 5 min) until a poll succeeds.

### Why the wire types are hand-written

Generating them from the Go structs (e.g. with
[tygo](https://github.com/gzuidhof/tygo)) was evaluated and rejected for now:

- the `/info`, `/config`, `/cluster` and `/flatcar/pin` response structs in
  `pkg/server` are unexported, and some handlers return `map[string]any`, so
  generation would need Go-side renames across the server package;
- tygo emits `any` for `time.Time`, `time.Duration` and interface fields and
  plain `string` for enum-like fields, losing the unions the UI switches on;
- the `Raw*` types are deliberately looser than the Go structs so the UI
  tolerates older servers, which a generator cannot express;
- generating only `pkg/hardware` and the autopilot controller would leave two
  sources of truth.

Keep `src/types/*` in sync with the Go structs by hand; the normaliser tests
in `src/__tests__/types.spec.ts` pin the expected shapes.

## Fleet status

Each `Host` carries three fleet fields reported by the running machine:
`running` (the version it last reported, or `image@digest` for ostree hosts),
`lastCheck` (RFC3339) and `rebootPending`. The Hosts table shows them as the
**Status** badge (`Reboot pending` / `Up to date` / `Unknown`), a monospace
**Running** column (digests are shortened to `image@<12 hex>` with the full
value in the tooltip) and a relative **Last check** column; Ignition file,
OSTree image and the install flag are folded into the Host cell to keep the
table readable at 1280px. Hosts from older servers that omit the fields are
normalised to `""`/`false` in `@/types`. `hostReporting(host, now)` in
`@/types` derives `never` (no `lastCheck`, `booted` or `health` at all: the
host booted before the `update`/`health` builtin units existed, or with
`--builtin=none`), `stale` (`lastCheck` older than 30 min) or `reporting`;
`versionMatch(host, targets)` compares `running` (or the host's
`targetVersion`) with the per-OS fleet target from `GET /info`'s `targets`
block (`fleetTargets(info)` falls back to the `version` blocks for older
servers). The Running cell renders a muted **no agent yet** badge
(`NoAgentBadge`, tooltip explains the reboot) for `never` and hides the Status
badge, the version with a green ✓ or a `behind <target>` warning when
reporting, and the version plus `last seen Xm ago` in warning colour when
stale; `—`/`Unknown` only remain for a reporting host without a version. The
host form repeats the badge on its read-only Running line. The Overview
page's Fleet panel takes `hosts`/`pendingReboots` from `GET /info`'s `fleet`
block when present and derives them from `/booty.json` otherwise, listing every
host pending reboot with its running and target version, and adds a muted
`N hosts have no agent yet` line when any host is in that state. The MAC link in the
Hosts table opens the merged Ignition preview
(`/ignition.json?mac=<mac>&preview=1&part=merged`); the small "user config" and
"builtin" links underneath open `part=user` and `part=builtin`.

## Install disk

`Host.installDisk` (e.g. `/dev/sda`, normalised to `""` when the server omits
it) is the target disk for the installer; empty lets it pick the first writable
disk. `HostForm` shows the **Install disk** input only for `bluefin` and
`coreos` hosts (the two OSes whose installer accepts a target), and the Hosts
table folds a set value into the Host cell as `disk /dev/sda`. The Overview and
About pages show the cached Bluefin release from `GET /info`'s `bluefin` block
(`—` until Booty has downloaded one).

## Secure Boot

`Host.secureBoot` (optional; older servers omit it) is set by the server when
the host's last boot script came through the Secure Boot path (UEFI HTTP Boot,
signed iPXE) and cleared by a plain PXE fetch. The Hosts table shows it as a
**Secure Boot** shield badge next to the OS badge; it is read-only in the form
and simply echoed back to `/register` with the rest of the host. `GET /info`'s
`secureBoot` block (`normalizeSecureBootInfo` in `@/types`) feeds the Overview
page's **Secure Boot** card: state (`off` / `syncing artefacts` / `ready`),
bundle version, boot URL, trusted CAs, the Flatcar CA fingerprint with copy and
`.der` download buttons, and the server's warnings about hosts that cannot boot
through Secure Boot. The card is always rendered so the feature is
discoverable; with `enabled: false` it collapses to the one-line off state.

## Autopilot

`AutopilotView` renders `GET /autopilot` (`normalizeAutopilotStatus` in
`@/types`), polled every 30 s like the Overview: mode, actuator, cluster and
attention counters as stat cards, a fleet-target table per OS (fleet target
with a **held** badge, `current`, `lastGood`; the releases that need an eye on
them -- anything not `good`, or `failing` -- listed with their state badge,
since when, attempts and last class, the OS's `good` releases behind a
**Show N good releases** toggle, an **N more in history → Storage** link when
`releaseCount` exceeds what the default answer carries, and a **Clear** button
on `quarantined`/`timeout` releases that POSTs
`/autopilot/{os}/release/{version}/clear` and reloads), the per-host episode
table (state badge, release → target, attempt, class, since, note; hostnames
come from `/booty.json`; a **Clear** button on `needs-hands`, `retrying` and
`rolled-back` rows opens an inline confirm and then POSTs
`/autopilot/host/{mac}/clear`, showing a refusal on the row), the reports and
the event timeline (newest first, `alert` events in red), 50 events at a time:
**Show more** goes to 100, then fetches the whole ring with
`/autopilot?events=all` (the poll keeps using it from then on) and shows
`eventCount` in the footer. A poll whose `revision`, event count and live
cluster facts equal the rendered ones leaves `status` untouched, and the
hostname map and the power ring are swapped only when their JSON differs, so a
quiet fleet re-renders nothing every 30 s. Each report row carries a
`draft`/`final` badge, a
**View** button that fetches `report.path` (`/autopilot/reports/<os>-<version>.md`,
via `apiGetText`) and shows the redacted Markdown in an inline `<pre>`, a raw
`.md` link, and, once the controller filed it, a **GitHub #n** link to
`report.postedURL` (or a `post failed` badge with `postError` as its tooltip).
With `mode: off` the page shows the summary and
an empty state. `Host.canary` is a checkbox in `HostForm` (every OS) and a
`canary` badge in the Hosts table; `Host.autopilot` (server-owned, echoed back
to `/register` untouched) shows as a state badge with the attempt number when
the host is not idle. `StorageView` joins the same records for its state
badges and fetches `/autopilot?releases=all`, so its **Show pruned release
history** switch has every record, not just the bounded default.

## Development

```sh
npm install
npm run dev          # http://localhost:5173/ui/
```

The dev server proxies every backend route (`/booty.json`, `/info`,
`/flatcar/*`, `/storage`, `/register`, `/unregister`, `/hosts`,
`/ignition.json`, `/cluster`, `/autopilot`, `/power`, `/config`, `/healthz`,
`/version.json`, `/data`) to a running Go
server. It defaults to `http://localhost:8080`; override with
`VITE_API_TARGET=http://host:port npm run dev`.

## Checks

```sh
npm run lint         # ESLint 9 flat config: eslint-plugin-vue flat/recommended + TS + Prettier
npm run type-check   # vue-tsc --build (views and tests are type-checked)
npm test             # Vitest + jsdom + @vue/test-utils, single run (test:unit watches)
npm run test:coverage   # same, with V8 coverage -> coverage/ (text summary, html, lcov, json-summary)
npm run format       # Prettier over src/
npm run format:check # what CI runs: fails on any file Prettier would change
npm run lint:check   # what CI runs: ESLint without --fix, zero warnings
```

## Build

```sh
npm run build        # type-check, then vite build -> dist/ (base path /ui/)
npm run preview      # serve dist/ locally
```

`dist/` is git-ignored and produced in the Docker image build.
