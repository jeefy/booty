# Cluster bootstrap from PXE

Booty provisions a Kubernetes cluster from bare metal: a Booty-managed control
plane or an externally managed one, on Flatcar, Fedora CoreOS and Bluefin
Server, with the distribution chosen per cluster.

## Decisions (user, 2026-09-26)

| Topic | Decision |
|---|---|
| Distribution | Per cluster: `kubeadm` or `k0s`. Flatcar/CoreOS support both; Bluefin supports `k0s` only (no containerd/kubelet in its `/usr`). |
| Control plane | `managed` (Booty renders the CP host) or `external` (today's join-only path). |
| CA / secrets | Booty generates the cluster CA (0600 in `--dataDir/cluster/`), serves private keys **only** to `role: control-plane` hosts. Optional bring-your-own via `--clusterCADir`. Everything renders before any node boots. |
| Cardinality | One cluster per Booty instance; per-host `role: control-plane \| worker` (default worker). |
| CNI | `--cni=cilium` (default) `\| calico \| flannel \| none`. Booty installs it from the first control plane, pinned version per CNI. |
| Verification | Go/web tests + config validators in CI; QEMU CP+worker boots by Sisyphus for flatcar/kubeadm, flatcar/k0s, coreos/kubeadm, bluefin/k0s. Harness stays outside the repo. |

## Out of scope (Must NOT have)

HA control planes (kube-vip / k0s CPLB / multiple CPs), kubeadm on Bluefin,
named multi-cluster, an in-repo e2e harness, cluster teardown/reset, upgrades,
Booty migrating itself into the cluster it built, a web UI editor for cluster
settings beyond what `/config` already shows (role is edited per host).
`controlPlaneEndpoint` is the only HA hook that ships.

## Facts the design rests on (verified)

- k0s custom CA is documented: `ca.{crt,key}`, `sa.{key,pub}`, `etcd/ca.{crt,key}` in `/var/lib/k0s/pki` before first start. `k0s token pre-shared --role worker|controller --cert ca.crt --url https://<endpoint>:6443 --valid <dur>` works offline; token = gzip+base64 kubeconfig bearing a bootstrap token; the matching bootstrap `Secret` must be applied on the first controller (`/var/lib/k0s/manifests/<dir>/`). k0s is a static binary embedding containerd/kubelet/etcd; runs on Flatcar/FCOS from `/opt/bin`.
- Bluefin: `/usr/bin/k0s` v1.36.4; `k0scontroller.service` = `k0s controller --enable-worker --single --disable-components=helm,autopilot`, no `-c`. `k0s-first-boot.service` runs every boot and does `daemon-reload` + `enable --now k0scontroller`. An `/etc` drop-in overriding `ExecStart=` wins. `--single` is incompatible with joins/etcd; argocd/kubestellar manifests are seeded on every controller.
- kubeadm: pre-placed `ca.crt/ca.key` are honoured; `bootstrapTokens[{token,ttl}]` and `certificateKey` live in v1beta4 config; discovery hash = SHA-256 of the CA SPKI (computable from `ca.crt`). Guard: `ConditionPathExists=!/etc/kubernetes/kubelet.conf`.
- Existing seams: `pkg/server/join.go` (`resolveJoinString`, `appendProfile`), `pkg/profile/kubeadm_worker.go` (`AppliesTo`, `Fragment`), `pkg/kubeadm/token.go` (`Minter`, in-cluster only), `pkg/creds/bundle.go` (Bluefin `tmpfiles.extra`; tests assert exactly two credentials), `pkg/server/config.go` (`secretSettings`).

## Configuration

New flags (all also `BOOTY_*` env), grouped under "Cluster":

| Flag | Default | Meaning |
|---|---|---|
| `--clusterDistribution` | `kubeadm` | `kubeadm` or `k0s` |
| `--controlPlane` | `external` | `managed` or `external` |
| `--controlPlaneEndpoint` | `""` | `host[:port]` all nodes use for the API. Required for `managed`; defaults to the CP host's IP when exactly one `role: control-plane` host is registered, else startup error. Documented as the HA hook (VIP/DNS). |
| `--clusterCADir` | `""` | BYO CA dir (kubeadm: `ca.crt/ca.key`; k0s: the six files). Empty → Booty generates into `--dataDir/cluster/pki/` on first `managed` start. |
| `--cni` | `cilium` | `cilium`, `calico`, `flannel`, `none` |
| `--cniRelease` | per-CNI pin | Overrides the pinned CNI release (`--cniVersion` already means containernetworking/plugins and keeps that meaning) |
| `--podCIDR` / `--serviceCIDR` | `10.244.0.0/16` / `10.96.0.0/12` | Cluster networks |
| `--k0sTokenFile` | `""` | External k0s control plane: pre-made worker join token |
| `--kubeconfig` | `""` | External kubeadm control plane, out-of-cluster token minting (`--kubeadmJoin=auto` outside the cluster) |

Compatibility: `--profile=kubeadm-worker`, `--joinString`, `--joinStringFile`,
`--kubeadmJoin` keep their meaning and equal `--clusterDistribution=kubeadm
--controlPlane=external`. A host without `role` renders **byte-identical** to
today (golden test). `--profile` becomes redundant when `--clusterDistribution`
is set explicitly; setting both to conflicting values is a startup error.

Validation at startup: `k0s` + Bluefin OK; `kubeadm` + any Bluefin host → the
Bluefin host gets HTTP 400 `unsupported distribution kubeadm for os bluefin` at
render time and a warning in `/config` (hosts can be registered later, so this
is per-request, not startup). `managed` without a `role: control-plane` host →
warning, workers render with `Restart=on-failure` join units and simply retry.

Per-host: `role` (`control-plane` | `worker`, default worker) in
`hardware.json`, `/register`, `/hosts`, UI host form (select) and host list
badge.

## Package layout

- `pkg/cluster/pki` — CA generation (RSA 2048 / ECDSA P-256; kubeadm uses RSA), SA key pair, etcd CA; load/create idempotently under `--dataDir/cluster/pki/` (dir 0700, files 0600); SPKI discovery hash; admin client cert + kubeconfig minting (`Admin(endpoint) ([]byte, error)`).
- `pkg/cluster/token` — kubeadm bootstrap token (`[a-z0-9]{6}.[a-z0-9]{16}`) generation and persistence (`--dataDir/cluster/tokens.json`, 0600, TTL 7d, regenerated when expired); k0s join token encoder (gzip+base64 kubeconfig with `certificate-authority-data`, bearer = bootstrap token, worker `:6443` / controller `:9443`) and the bootstrap `Secret` YAML. Fixture test against a captured `k0s token pre-shared` output (decode → compare kubeconfig fields, not bytes).
- `pkg/cluster` — `Settings` (from flags), `Plan(host) (Role, Distribution, error)`, and the per-distribution renderers below. Single entry point used by both the Ignition path and the Bluefin creds path so file contents are shared.
- `pkg/profile` — `kubeadm_worker.go` stays; add `kubeadm_control_plane.go` (init fragment) and `k0s.go` (controller/worker fragments, binary install to `/opt/bin/k0s` from the pinned k0s release, sha256-verified). `Fragment(host, opts)` selects by role and distribution; never appends worker units to a CP host.
- `pkg/cni` — pinned manifests/commands per CNI: `cilium` (cilium-cli `install --version` from the CP), `calico` (operator manifest URL), `flannel` (manifest URL). Rendered as a `booty-cni-install.service` oneshot on the CP host with `ConditionPathExists=!/var/lib/booty/cni-installed`, `Restart=on-failure`. For k0s: `cilium`/`flannel` → `spec.network.provider: custom` + the same oneshot; `calico` → k0s built-in; `none` → `custom`, nothing installed.
- `pkg/kubeadm` — `Minter` takes a `*rest.Config`; constructors `InCluster()`, `FromKubeconfig(path)`, `FromCA(pki, endpoint)` (managed mode: Booty's own admin kubeconfig).
- `pkg/server` — `role` plumbing, `GET /cluster` (settings, endpoint, CA fingerprint, per-host role/status; **no key material**), redaction of PKI paths in `/config`; the `?preview` responses for CP hosts include the key like the real render does (it is the config), but the UI preview panel masks `PRIVATE KEY` blocks.
- `pkg/creds` — Bluefin k0s files ride inside `tmpfiles.extra` (still exactly two credentials): `/etc/k0s/k0s.yaml`, PKI files (CP only), token file (worker), `/etc/systemd/system/k0scontroller.service.d/booty.conf` (`ExecStart=` reset + override), manifests Secret (CP), CNI oneshot.

## Node-side behaviour

### kubeadm, managed, control plane (Flatcar/FCOS)
Files: `/etc/kubernetes/pki/{ca.crt,ca.key}` (0600), `/etc/booty/kubeadm-init.yaml`
(v1beta4 `InitConfiguration` with `bootstrapTokens: [{token, ttl: 168h}]`,
`certificateKey`, `ClusterConfiguration` with `controlPlaneEndpoint`,
`kubernetesVersion=--k8sVersion`, `networking.{podSubnet,serviceSubnet}`).
Units: existing tool chain (`booty-cni-install` → `booty-kube-tools` →
`booty-kubelet-setup`), then `booty-k8s-init.service`
(`ConditionPathExists=!/etc/kubernetes/kubelet.conf`, `kubeadm init --config
… --upload-certs`, `Restart=on-failure RestartSec=30s`), then
`booty-cni-apply.service` (per `--cni`), then `booty-booted`. Reboots: kubelet
restarts static pods; init never reruns.

### kubeadm, worker (managed or external)
Today's `booty-k8s-join.service` plus `Restart=on-failure RestartSec=30s`.
Managed: join string = `kubeadm join <endpoint> --token <pre-generated>
--discovery-token-ca-cert-hash sha256:<from ca.crt>` until Booty can reach the
API with its admin kubeconfig, then `auto` minting as today (same `Minter`).
External: unchanged (`static` / `auto` in-cluster / `auto` via `--kubeconfig`).

### k0s, managed, controller (Flatcar/FCOS/Bluefin)
Files: `/var/lib/k0s/pki/{ca.crt,ca.key,sa.key,sa.pub,etcd/ca.crt,etcd/ca.key}`
(0600), `/etc/k0s/k0s.yaml` (`spec.api.externalAddress=<endpoint>`,
`spec.network.{podCIDR,serviceCIDR,provider}`), bootstrap Secret at
`/var/lib/k0s/manifests/booty/tokens.yaml`. Unit: Flatcar/FCOS →
`booty-k0s-install.service` (fetch pinned `k0s` to `/opt/bin`, verify sha256)
then `k0scontroller.service` (`k0s controller -c /etc/k0s/k0s.yaml
--enable-worker`, `Restart=always`, guard `!/var/lib/k0s/pki/admin.conf` is not
needed: k0s is idempotent). Bluefin → drop-in overriding `ExecStart` to the
same command (drops `--single`; keeps `--disable-components=helm,autopilot`).
Bluefin's argocd/kubestellar seeding is left alone and documented.

### k0s, worker (managed or external)
`/etc/k0s/token` (0600) with the pre-shared (managed) or `--k0sTokenFile`
(external) token; unit `k0s worker --token-file /etc/k0s/token`
(Flatcar/FCOS: own `k0sworker.service`; Bluefin: drop-in on
`k0scontroller.service` since that is what `k0s-first-boot` starts).

### Common
`controlPlaneEndpoint` in every rendered config; `booty-booted` unchanged; the
CP host additionally POSTs `/cluster/ready` (no secrets; sets `ready` in
`GET /cluster` so the UI can show it). Nothing gates worker boots.

## API and UI

- `POST /register`, `/hosts`: accept/return `role`.
- `GET /cluster`: `{distribution, controlPlane, endpoint, cni, ready, caFingerprint, hosts:[{mac,hostname,role,os,booted}], warnings:[...]}`.
- `POST /cluster/ready?mac=` from the CP host.
- `GET /config`: new settings appear; `clusterCADir`, `k0sTokenFile`, `kubeconfig` are path-only; PKI/token file *contents* never appear.
- UI: role select in `HostForm`, role badge in the hosts list, a "Cluster" card on Home (`GET /cluster`), `PRIVATE KEY` masking in preview panes.
- README: "Cluster bootstrap" section (matrix, flags, CA trust note, CNI table, Bluefin caveats), Trust model paragraph about the CA key on the boot VLAN and how to avoid it (`--controlPlane=external`, or VLAN-isolate the CP host's first boot).

## Slices / PRs

1. **PR H1 — model + PKI + tokens**: `role` everywhere (API/UI/tests), `pkg/cluster/{pki,token}`, flags + validation, `Minter` generalisation (`FromKubeconfig`, `FromCA`), `GET /cluster`, redaction. No rendering changes; legacy golden test added. Deploy to homelab as a no-op check (`external`).
2. **PR H2 — kubeadm managed CP + CNI**: `kubeadm_control_plane.go`, `pkg/cni`, worker join `Restart=`, managed join-string path, `/cluster/ready`. QEMU: flatcar CP + flatcar worker + **coreos worker** → `kubectl get nodes` Ready with cilium (then calico, flannel smoke via render tests + one boot each if time permits, else render-only and noted).
3. **PR H3 — k0s**: `pkg/profile/k0s.go`, Bluefin drop-ins via `tmpfiles.extra`, `--k0sTokenFile`. QEMU: bluefin controller + flatcar k0s worker; and flatcar k0s controller + bluefin worker.
4. **PR H4 — docs + homelab**: README, `~/Code/homelab/booty` docs (no flag changes needed; homelab stays `external`), plan evidence section with the pasted `kubectl get nodes` outputs.

Each PR: CI green before merge, image built/pushed, homelab rolled, fleet checked
(`kubectl get nodes`, `/info` fleet, a real `aren` PXE reboot after H2 since
the worker join unit changes).

## Acceptance (agent-executable unless marked)

- `go test ./... -race`, `golangci-lint run`, web lint/tests/build green.
- Golden: host without `role`, legacy flags → Ignition byte-identical to `main` (`git show main:` fixture).
- `curl -s "$B/ignition/builtin.json?mac=$CP&preview=1" | jq -r '.storage.files[].path'` contains `/etc/kubernetes/pki/ca.key` for a CP host; for a worker `grep -c "PRIVATE KEY"` on the whole preview = 0.
- `curl -s $B/config | grep -c "PRIVATE KEY"` = 0; `GET /cluster` = 0.
- Discovery hash: Go == `openssl x509 -pubkey -noout -in ca.crt | openssl pkey -pubin -outform der | sha256sum`.
- Rendered kubeadm config: `kubeadm config validate --config` exit 0 (pinned kubeadm in CI). Rendered k0s config: `podman run --rm -v k0s.yaml:/k0s.yaml docker.io/k0sproject/k0s:v1.36.4 k0s config validate -c /k0s.yaml` exit 0.
- k0s token: Go-encoded token decodes to a kubeconfig whose server/CA/bearer match; captured `k0s token pre-shared` fixture decodes with the same Go decoder.
- `ignition-validate` exit 0 on every rendered config; Bluefin bundle decrypts and the drop-in contains `ExecStart=` twice and no `--single`.
- 400 `unsupported distribution kubeadm for os bluefin` for a Bluefin host under `kubeadm`.
- Startup refuses `--controlPlane=managed` with an unreadable `--clusterCADir` (stderr mentions `cluster CA`).
- **QEMU (Sisyphus, manual, evidence pasted here)**: for each matrix row above, `kubectl get nodes -o wide` shows the CP and worker `Ready` within 10 minutes of power-on with both VMs started simultaneously; a second boot of each VM keeps them `Ready` (no re-init/re-join damage).

## Risks

- CA key on the boot VLAN — accepted by the user; documented with mitigations. Booty's `--dataDir/cluster/` is cluster-admin: called out in README and `/config`.
- FCOS kubeadm path has never booted here; H2 QEMU includes it — if `rpm-ostree`/`dnf` layering fails, fix in H2 rather than ship unverified.
- Bluefin `k0s-first-boot` ordering with our drop-in: proven pattern (the `Wants=` drop-in from PR #30 already works); the ExecStart override is the same mechanism.
- k0s pre-shared token format drift across versions: fixture-tested against the pinned v1.36.4 output.
