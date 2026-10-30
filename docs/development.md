# Examples and Development

Example configs, building and testing Booty, and project notes.

## Examples

[Example ignition configs and Kubernetes manifest](../examples/README.md)

### Docker

```
docker run --rm -it \
  --network=host \
  --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  -v $PWD:/data/ \
  ghcr.io/jeefy/booty:v1.1.0 \
  --profile=kubeadm-worker \
  --joinString="kubeadm join 192.168.1.10:6443 --token ${TOKEN} --discovery-token-ca-cert-hash sha256:${SHA_HASH}" \
  --serverIP=192.168.1.10 \
  --serverHttpPort=8080 \
  --flatcarChannel=beta \
  --coreOSChannel=testing
```

The image runs as nonroot (uid 65532; `:main` and v1.1.0 and later) with file capabilities; the mounted `/data` must be writable by that uid, and the bounding set must keep `NET_BIND_SERVICE` and `NET_RAW` (see [Running as root / capabilities](trust-model.md#running-as-root--capabilities)).

### Kubernetes

[Example deployment](../examples/k8s.yaml)

Booty in `kube-system` on the control-plane node with `hostNetwork`, `--profile=kubeadm-worker --kubeadmJoin=auto`, the ServiceAccount and Roles the token minting needs, a ConfigMap with the site-only Butane, a Secret with the SSH keys mounted for `--sshAuthorizedKeysFile`, `/healthz` probes, `runAsNonRoot` with `fsGroup: 65532` so the nonroot image can write its data directory, `NET_BIND_SERVICE`+`NET_RAW` only, and two MetalLB Services sharing one VIP. Set `--serverIP` to the address booting machines actually reach.

## Development

```
make build      # builds the Web UI, then the Go binary with it embedded -> bin/booty
make run        # build + run against ./data with --debug
make test       # test-go + test-web
make test-go    # go test -race -timeout=5m
make test-web   # Vitest
make lint       # lint-go + lint-web
make lint-go    # golangci-lint
make lint-web   # eslint + prettier --check + vue-tsc
make fmt-check  # fails when any Go file is not gofmt-clean (for CI)
make image      # multi-stage container build (VERSION/TIMESTAMP stamped into /info)
make ipxe       # rebuild boot/*.kpxe|*.efi from the pinned iPXE release in a container (only when bumping iPXE or editing boot/embed.ipxe)
```

Builds require **Go >= 1.26.10** (the `go` directive in `go.mod`; the floor fixes 17 standard-library CVEs). The Web UI's Node version is single-sourced in [`web/.nvmrc`](../web/.nvmrc) (Node 26), which both CI and local builds read.

The iPXE binaries in `boot/` are committed, so a plain `go build` needs no cross toolchain and works air-gapped. `make ipxe` (see [hack/build-ipxe.sh](../hack/build-ipxe.sh)) rebuilds them reproducibly; verify with `cd boot && sha256sum -c SHA256SUMS`. Licensing details are in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

The Web UI lives in `web/` (Vue 3 + Vite). `cd web && npm run dev` starts a dev server that proxies API calls to a Booty running on `localhost:8080` (override with `VITE_API_TARGET`). See [web/README.md](../web/README.md).

### CI

`.github/workflows/docker-publish.yml` runs tests, the multi-arch image build,
SLSA provenance and its verification. Docs-only changes (Markdown, `docs/`,
`LICENSE`, `.gitignore`) skip all of that through the `changes` job; `v*.*.*`
tags always build. Publishing the GitHub release does not trigger a second
build, so a release tag, its `major.minor` tag and `latest` share one digest.
CI lints the web UI with `npm run lint:check` (no `--fix`, zero warnings) and
`npm run format:check` (Prettier over `web/src/`; fix with `npm run format`).
The commit that first applied Prettier is listed in `.git-blame-ignore-revs`
(`git config blame.ignoreRevsFile .git-blame-ignore-revs`).
