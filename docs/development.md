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
  ghcr.io/jeefy/booty:main \
  --profile=kubeadm-worker \
  --joinString="kubeadm join 192.168.1.10:6443 --token ${TOKEN} --discovery-token-ca-cert-hash sha256:${SHA_HASH}" \
  --serverIP=192.168.1.10 \
  --serverHttpPort=8080 \
  --flatcarChannel=beta \
  --coreOSChannel=testing
```

The image runs as nonroot (uid 65532; `:main` and releases after 1.0.0) with file capabilities; the mounted `/data` must be writable by that uid, and the bounding set must keep `NET_BIND_SERVICE` and `NET_RAW` (see [Running as root / capabilities](trust-model.md#running-as-root--capabilities)).

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
make lint-web   # eslint + vue-tsc
make fmt-check  # fails when any Go file is not gofmt-clean (for CI)
make image      # multi-stage container build (VERSION/TIMESTAMP stamped into /info)
make ipxe       # rebuild boot/*.kpxe|*.efi from the pinned iPXE release in a container (only when bumping iPXE or editing boot/embed.ipxe)
```

Builds require **Go >= 1.26.10** (the `toolchain` directive in `go.mod`; the floor fixes 17 standard-library CVEs). The Web UI's Node version is single-sourced in [`web/.nvmrc`](../web/.nvmrc) (Node 26), which both CI and local builds read.

The iPXE binaries in `boot/` are committed, so a plain `go build` needs no cross toolchain and works air-gapped. `make ipxe` (see [hack/build-ipxe.sh](../hack/build-ipxe.sh)) rebuilds them reproducibly; verify with `cd boot && sha256sum -c SHA256SUMS`. Licensing details are in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

The Web UI lives in `web/` (Vue 3 + Vite). `cd web && npm run dev` starts a dev server that proxies API calls to a Booty running on `localhost:8080` (override with `VITE_API_TARGET`). See [web/README.md](../web/README.md).

### CI

`.github/workflows/docker-publish.yml` runs tests, the multi-arch image build,
SLSA provenance and its verification. Docs-only changes (Markdown, `docs/`,
`LICENSE`, `.gitignore`) skip all of that through the `changes` job; tags and
releases always build.
