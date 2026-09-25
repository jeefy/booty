# Booty web UI

Vue 3 + TypeScript + Vite single-page app served by the Go binary under `/ui/`
(the server does `http.FileServer(http.Dir("./web/dist"))`, so `npm run build`
output must exist before starting `booty`).

## Layout

```
src/
  api.ts          typed fetch helpers (apiGet / apiPost) + ApiError from the JSON error envelope
  types.ts        wire types mirroring the Go API (Host, UnknownHost, BootyData, Info, PinState, CachedImage)
  utils/time.ts   relative / absolute time formatting for RFC3339 timestamps
  components/     ErrorAlert, LoadingState, EmptyState, HostForm
  views/          HomeView (status + Flatcar pin), HostsView, CacheView, AboutView
  router/         hash-based routes (/, /hosts, /cache, /about)
  assets/main.css design tokens (CSS variables) layered on top of Bootstrap 5
```

Bootstrap CSS is imported from the npm package in `src/main.ts`; no Bootstrap
JS is used (the navbar collapse is driven by Vue state).

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
