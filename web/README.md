# Booty web UI

Vue 3 + TypeScript + Vite single-page app served by the Go binary under `/ui/`
(the server does `http.FileServer(http.Dir("./web/dist"))`, so `npm run build`
output must exist before starting `booty`).

## Layout

```
src/
  api.ts          typed fetch helpers (apiGet / apiPost) + ApiError from the JSON error envelope
  types.ts        wire types mirroring the Go API (Host, UnknownHost, BootyData, Info, PinState, CachedImage) + normalisers
  utils/time.ts   relative / absolute time formatting for RFC3339 timestamps
  utils/fleet.ts  fleet helpers: running-label splitting, host status, pending list, preview URLs
  components/     ErrorAlert, LoadingState, EmptyState, HostForm
  views/          HomeView (status + Flatcar pin), HostsView, CacheView, AboutView
  router/         hash-based routes (/, /hosts, /cache, /about)
  assets/main.css design tokens (CSS variables) layered on top of Bootstrap 5
```

Bootstrap CSS is imported from the npm package in `src/main.ts`; no Bootstrap
JS is used (the navbar collapse is driven by Vue state).

## Fleet status

Each `Host` carries three fleet fields reported by the running machine:
`running` (the version it last reported, or `image@digest` for ostree hosts),
`lastCheck` (RFC3339) and `rebootPending`. The Hosts table shows them as the
**Status** badge (`Reboot pending` / `Up to date` / `Unknown`), a monospace
**Running** column (digests are shortened to `image@<12 hex>` with the full
value in the tooltip) and a relative **Last check** column; Ignition file,
OSTree image and the install flag are folded into the Host cell to keep the
table readable at 1280px. Hosts from older servers that omit the fields are
normalised to `""`/`false` in `types.ts` and render as `Unknown`. The Overview
page's Fleet panel takes `hosts`/`pendingReboots` from `GET /info`'s `fleet`
block when present and derives them from `/booty.json` otherwise, listing every
host pending reboot with its running and target version. The MAC link in the
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

## Development

```sh
npm install
npm run dev          # http://localhost:5173/ui/
```

The dev server proxies every backend route (`/booty.json`, `/info`,
`/flatcar/*`, `/registry`, `/register`, `/unregister`, `/hosts`,
`/ignition.json`, `/healthz`, `/version.json`, `/data`) to a running Go
server. It defaults to `http://localhost:8080`; override with
`VITE_API_TARGET=http://host:port npm run dev`.

## Checks

```sh
npm run lint         # ESLint 9 flat config: eslint-plugin-vue flat/recommended + TS + Prettier
npm run type-check   # vue-tsc --build (views and tests are type-checked)
npm run test:unit -- --run   # Vitest + jsdom + @vue/test-utils
npm run format       # Prettier over src/
```

## Build

```sh
npm run build        # type-check, then vite build -> dist/ (base path /ui/)
npm run preview      # serve dist/ locally
```

`dist/` is git-ignored and produced in the Docker image build.
