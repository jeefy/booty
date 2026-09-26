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

## Addendum (user, 2026-09-26, decided during H2): the control-plane disk

PXE-booted Flatcar and Fedora CoreOS run from RAM, so a managed control
plane needs **`--controlPlaneDisk=/dev/…`**. Ignition formats the device
once (`ext4`, label `booty-cp`, `wipe_filesystem: false`: an existing
`booty-cp` filesystem is reused, a foreign one makes Ignition refuse to
boot), mounts it at `/var/lib/booty-cp` (`var-lib-booty\x2dcp.mount`) and
bind-mounts `/etc/kubernetes`, `/var/lib/etcd` and `/var/lib/kubelet` from
subdirectories via systemd mount units (`etc-kubernetes.mount`,
`var-lib-etcd.mount`, `var-lib-kubelet.mount`) that `RequiresMountsFor=`
the disk; the kubelet (drop-in), `booty-kubelet-setup` and
`booty-k8s-init` in turn `RequiresMountsFor=` the three bind mounts.
Because Ignition writes files to the RAM root before any mount unit runs,
`ca.crt`/`ca.key` are still rendered at `/etc/kubernetes/pki/` and a
`booty-cp-seed.service` (`DefaultDependencies=no`, ordered between the disk
mount and the bind mounts) copies them onto the disk with `cp -an`, so the
disk copy wins on later boots. Rendering a managed kubeadm control plane
for a flatcar/coreos host **without** `--controlPlaneDisk` is HTTP 400
`control-plane host needs --controlPlaneDisk on a PXE-booted OS` (all three
Ignition endpoints) plus a `GET /cluster` warning. The flag mirrors
`--containerdDisk` (same validation as `installDisk`); the two may not name
the same device (startup error). Bluefin installs to disk and is exempt
(k0s, H3).

### H2 implementation notes (decided where the plan was silent)

- `certificateKey` is persisted in `cluster/tokens.json` under the purpose
  `kubeadm-cert-key` (64 hex chars, 32 random bytes); the token store now
  validates per purpose. It renews with the same 7-day TTL as the tokens,
  which is harmless: init runs once and the uploaded certs expire after 2 h.
- `booty-cni-apply.service` and `booty-cluster-ready.service` only `Wants=`
  `booty-k8s-init.service` (not `Requires=`): a failing init keeps
  restarting, and a `Requires=` dependant would have had its job cancelled
  by the first failure. Both carry their own `Restart=on-failure` loop and
  check for `/etc/kubernetes/admin.conf` themselves.
- The CNI marker is `/var/lib/booty-cp/cni-applied` (on the disk) rather than
  `/var/lib/booty/cni-applied`, which would be RAM on a PXE host and re-apply
  on every boot. The unit is named `booty-cni-apply.service` as in the H2
  task (the plan said `booty-cni-install`, which is already the plugins
  unit).
- flannel: the upstream `kube-flannel.yml` is downloaded at apply time and,
  when `--podCIDR` is not `10.244.0.0/16`, its single `"Network"` entry is
  rewritten with a guard (the script fails if the manifest does not have
  exactly one). Vendoring a copy would have pinned the manifest independently
  of `--cniRelease`.
- `ready` means "kubeadm init finished and `/readyz` answers on the control
  plane" -- the moment workers can join. It is persisted in
  `cluster/ready.json`; the first `readyAt` sticks (idempotent POST).
- Worker `join.sh` exits 0 when `/etc/kubernetes/kubelet.conf` exists; the
  join unit gained `Restart=on-failure RestartSec=30s`. The legacy golden
  fixtures were refreshed for exactly these two changes.
- A managed kubeadm cluster implies `--profile=kubeadm-worker` for its
  workers. `--kubeadmJoin=static` without `--joinString` under `managed`
  uses the pre-generated join string; an explicit `--joinString` still wins.
- A `role: control-plane` host never receives worker join units, also in
  external mode (it renders only the builtin fragment there); it never mints
  a join token either.
- The 400 `unsupported distribution kubeadm for os bluefin` applies with
  `--controlPlane=managed` only. In external mode (the default) Bluefin
  hosts keep receiving the plain builtin fragment next to a kubeadm-worker
  profile, as the existing tests and homelab rely on.
- The kubelet's `cgroup-driver` is set through `KubeletConfiguration` (and
  `/etc/default/kubelet`, shared with the worker) rather than
  `kubeletExtraArgs`, which kubeadm warns about as a deprecated flag; only
  `fail-swap-on=false` is passed as an extra arg.
- CNI pins verified against the GitHub releases API on 2026-09-25: cilium
  v1.20.2 + cilium-cli v0.20.1
  (sha256 `24e817dcfcc8a12e325ce7547617bcbcf171c5b0b335bb24d2bb1207ba047f61`),
  calico v3.32.2, flannel v0.28.9.
- **Fedora CoreOS, measured on a live-PXE FCOS 44 VM (2026-09-26)**: `dnf
  install` and `rpm-ostree usroverlay` both fail (`Remounting /boot
  read-write: Invalid argument`; `/usr` is `erofs ro`), so the
  `VARIANT_ID=fedora` / `pkgs.k8s.io` branch of `kube-tools.sh` could only
  ever have worked on a disk-installed FCOS and is removed: every OS gets the
  static `dl.k8s.io` binaries in `/opt/bin` (`/opt -> /var/opt` is writable).
  FCOS ships `/usr/bin/containerd` (2.3.4) with `containerd.service`
  disabled, so a new `booty-containerd-setup.service` (between kube-tools and
  kubelet-setup, worker and control plane alike) regenerates
  `/etc/containerd/config.toml` with `SystemdCgroup = true` (guarded sed on
  `containerd config default`, stock file kept as `.booty-orig`) and enables
  it; on Flatcar, whose unit runs `--config /usr/share/containerd/config.toml`
  with `SystemdCgroup = true` already, it is a no-op that never writes
  `/etc/containerd/config.toml`. The legacy golden fixtures were refreshed
  for exactly this change. The FCOS "rpm-ostree layering" risk above is
  therefore moot; the kubelet unit templates rewritten to `/opt/bin/kubelet`
  were checked against the current krel `master` templates.

### H3 implementation notes (decided where the plan was silent)

- The shared k0s renderer lives in its own package `pkg/cluster/k0s`
  (`Render(Options) (*Node, error)`, `Node{Files, Units, DropIns}`) rather
  than as a function on `pkg/cluster`, because `pkg/cluster` imports
  `pkg/profile` and `pkg/profile` must import the renderer too (an import
  cycle otherwise). `cluster.Manager.K0sNodeFiles(hosts, host, server)` is
  the single entry point that gathers PKI, tokens, endpoint and CNI and
  calls it; `pkg/profile/k0s.go` (Ignition) and `pkg/creds` (Bluefin
  `tmpfiles.extra`) only translate the `Node`. It returns `nil, nil` for a
  control-plane host under an external control plane and for a worker
  without `--k0sTokenFile` in external mode (plain builtin fragment, as
  before); errors are render refusals (400 on the Ignition endpoints, logged
  and dropped on `/creds/` so the Bluefin install itself is not aborted).
- `--k0sVersion` (new, default `v1.36.4+k0s.0` = Bluefin 26.08.0's
  `/usr/bin/k0s`) is validated as a k0s tag at startup when the distribution
  is k0s. Asset facts verified on 2026-09-26 with `gh api
  repos/k0sproject/k0s/releases/tags/v1.36.4%2Bk0s.0`: the binary is
  `k0s-v1.36.4+k0s.0-amd64` (262445792 bytes,
  `https://github.com/k0sproject/k0s/releases/download/v1.36.4%2Bk0s.0/k0s-v1.36.4%2Bk0s.0-amd64`),
  the checksums are in `sha256sums.txt` (there is no per-asset `.sha256`;
  `.sig` files are cosign signatures) and the amd64 line reads
  `ca1e9e68107335846e8296777fce2ccd654284e6265b4b5d32c34ead872af98f`, equal
  to GitHub's asset `digest`. That value is pinned in `k0s.DefaultSHA256`;
  any other `--k0sVersion` makes the install script fetch that release's
  `sha256sums.txt` and verify against it, refusing to install on a mismatch
  or a missing line.
- `--cni` on k0s: `none` -> `spec.network.provider: kuberouter` (k0s's
  default, node Ready out of the box; the plan said `custom`, which would
  have left the node without a network), `calico` -> built-in `calico`,
  `cilium`/`flannel` -> `custom` plus the existing `booty-cni-apply.service`.
  `pkg/cni.Render` now takes a `Target{After, Weak, Kubectl, Kubeconfig,
  Marker}`; the kubeadm target (`cni.Kubeadm(after)`) renders exactly the
  bytes it did before, the k0s target prepends a `kubectl() { <binary>
  kubectl "$@"; }` shell function so the plugin scripts themselves are
  unchanged, uses `Wants=` instead of `Requires=` on `k0scontroller.service`
  (a `Restart=always` service must not take the oneshot down with it) and
  the marker `/var/lib/booty/cni-applied` on Bluefin.
- PXE controller disks: `/var/lib/k0s` is bind-mounted from
  `/var/lib/booty-cp/k0s` (`var-lib-k0s.mount`) after `booty-cp-seed.service`
  ran `k0s-seed.sh` (`cp -an` of the Ignition-written `/var/lib/k0s/.`, then
  a forced copy of `manifests/booty/` so a controller reboot re-applies the
  Secret Booty currently renders). `--containerdDisk`, when set, is mounted
  at `/var/lib/k0s/containerd` (`var-lib-k0s-containerd.mount`, label `ssd`,
  wiped per boot) like the kubeadm control plane's `/var/lib/containerd`.
  A PXE worker mounts the wiped `--containerdDisk` at `/var/lib/k0s` itself
  (k0s's extracted binaries, containerd and kubelet state all live there and
  do not fit a 3 GiB tmpfs root). The `RenderCheck`/`Warnings` disk rule is
  now distribution-agnostic (`NeedsControlPlaneDisk(os)` already exempted
  Bluefin).
- All six PKI files are 0600 (also `ca.crt`/`sa.pub`); k0s runs as root
  and does not mind. The tokens manifest carries both the worker and the
  controller Secret (two documents, `---`).
- PXE units follow the template `k0s install` writes (`Delegate=yes`,
  `KillMode=process`, `LimitNOFILE`, `Restart=always RestartSec=10`) with
  `Requires=/After=booty-k0s-install.service` and
  `RequiresMountsFor=/var/lib/k0s`. `booty-k0s-install.service` is a
  `Restart=on-failure RestartSec=15s` oneshot: a matching `/opt/bin/k0s` is
  left alone, downloads go to a temp file next to it and are `mv`ed only
  after the checksum passes.
- Bluefin: the k0s pieces ride in `tmpfiles.extra` after the existing rules
  (the non-k0s output is byte-identical, asserted by the old exact-match
  test), with `d` lines for `/etc/k0s`, `/var/lib/k0s{,/pki,/pki/etcd,
  /manifests,/manifests/booty}` (0755; files carry their own modes). The
  `k0scontroller.service.d` directory is created once and holds both
  `booty-role.conf` (`[Service]\nExecStart=\nExecStart=...`, no `--single`)
  and the existing `booty.conf` (`Wants=`, now also listing
  `booty-cluster-ready.service` and, when custom, `booty-cni-apply.service`).
  Like `--profile`, the k0s pieces are off under `--builtin=none`.
- `resolveJoinString` under k0s: never mints, never warns, never falls back
  to the kubeadm pre-generated string; a static `--joinString` still passes
  through as the template variable the operator asked for. `profileOptions`
  dispatches on distribution before the kubeadm branch and forces
  `Profile=""` (startup already refuses `--profile` with k0s).
- Known gap (documented in the README): the k0s worker token rotates with
  the 7-day store TTL, but Booty has no k0s equivalent of the kubeadm
  `Minter` to re-apply the Secret through the API. PXE workers rejoin on
  every boot; after a rotation they need the controller rebooted (PXE
  controllers re-seed `manifests/booty/`) or the Secret re-applied by hand.
  Candidate for H4: apply the Secret with an admin kubeconfig from Booty's
  CA when `/cluster/ready` is set.
- Verified during H3: `podman run --rm -v k0s.yaml:/k0s.yaml:ro,Z
  docker.io/k0sproject/k0s:v1.36.4-k0s.0 k0s config validate -c /k0s.yaml`
  exits 0 for all eight rendered configs (4 providers x endpoint with/without
  port) and rejects `provider: bogus` (`Unsupported value: "bogus": supported
  values: "kuberouter", "calico", "custom"`), so the check is real; the test
  `TestConfigValidatesWithK0s` runs it whenever the image is cached
  (`K0S_PULL=1` pulls). The plan's `v1.36.4-k0s.1` image tag is not what
  Bluefin runs; `v1.36.4-k0s.0` is.

## Evidence: H2 QEMU run (Sisyphus, 2026-09-25/26, bridged lab `br-booty` 10.77.0.0/24)

Booty `feat/cluster-h2` on the host, `--controlPlane=managed --controlPlaneEndpoint=10.77.0.30 --containerdDisk=/dev/vda --controlPlaneDisk=/dev/vdb --cni=cilium --profile=kubeadm-worker --kubeadmJoin=auto --flatcarVersion=4757.2.0`. Three OVMF/KVM VMs powered on together: `cp1` (Flatcar, `role: control-plane`, 4 GiB, two 20 GiB disks), `w-flatcar` (Flatcar, 3 GiB, one disk), `w-fcos` (Fedora CoreOS 44, 3.5 GiB, one disk). Flatcar and FCOS PXE-boot from RAM; dnsmasq reserved 10.77.0.30 for the CP MAC.

`kubectl get nodes -o wide` 7 minutes after power-on (from `cp1`, `/etc/kubernetes/admin.conf`):

```
NAME        STATUS   ROLES           AGE     VERSION   INTERNAL-IP   OS-IMAGE                                      KERNEL-VERSION           CONTAINER-RUNTIME
cp1         Ready    control-plane   5m29s   v1.34.3   10.77.0.30    Flatcar Container Linux by Kinvolk 4757.2.0   6.12.109-flatcar         containerd://2.2.5
w-fcos      Ready    <none>          5m15s   v1.34.3   10.77.0.134   Fedora CoreOS 44.20260829.3.1                 7.1.10-200.fc44.x86_64   containerd://2.3.4
w-flatcar   Ready    <none>          5m18s   v1.34.3   10.77.0.133   Flatcar Container Linux by Kinvolk 4757.2.0   6.12.109-flatcar         containerd://2.2.5
```

All 16 pods Running (Cilium 1.20.2 installed by `booty-cni-apply`, marker on the CP disk), `GET /cluster` `ready: true`, `booty-k8s-init` finished once. **Reboot idempotence**: all three VMs `system_reset` at once; 6 minutes later the same three nodes are `Ready`, `kube-system` namespace UID unchanged (`8814e4b9-b262-454d-89fb-e8d44864ff24` before and after), a ConfigMap created before the reboot is still there, `booty-k8s-init` skipped on `ConditionPathExists=!/etc/kubernetes/kubelet.conf`.

Findings that changed the code or docs:
- Live-PXE FCOS cannot `dnf install` (`/usr` is read-only erofs; bootc status error) → static `dl.k8s.io` binaries on every OS, `booty-containerd-setup.service` enables FCOS's shipped containerd with `SystemdCgroup = true` (no-op on Flatcar).
- cilium-cli needs `HOME` → `Environment=HOME=/root XDG_CACHE_HOME=/var/cache/booty-cni` on `booty-cni-apply.service`.
- Without `--containerdDisk`, a 3 GiB PXE worker's tmpfs root filled to 96 % with images (`ImagePullBackOff`) → documented; the lab now gives every node a containerd disk, as the homelab does.
- Lab-only: VM disks on a tmpfs `/tmp` under memory pressure produced ext4 journal aborts on the guests; disks moved to real storage.
