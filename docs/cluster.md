# Cluster Bootstrap (kubeadm and k0s)

Booty provisions a whole Kubernetes cluster from PXE: managed or external control planes, kubeadm or k0s, on Flatcar, Fedora CoreOS and Bluefin Server.

## Cluster bootstrap

Booty provisions a whole Kubernetes cluster from PXE: it generates the cluster CA, renders a control plane onto a registered host and joins every other host as a worker, on Flatcar, Fedora CoreOS and Bluefin Server. You pick the distribution per cluster with `--clusterDistribution` (`kubeadm`, the default, or `k0s`) and decide who runs the control plane with `--controlPlane`: `managed` means Booty renders the control plane itself, `external` (the default) is the join-only behaviour from the previous sections, pointing at a cluster you already have. The design and the QEMU evidence for every supported combination are in [docs/plans/2026-09-26-cluster-bootstrap.md](plans/2026-09-26-cluster-bootstrap.md).

| OS | kubeadm control plane | kubeadm worker | k0s control plane | k0s worker |
|---|---|---|---|---|
| Flatcar | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) |
| Fedora CoreOS | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) |
| Bluefin Server | not supported (HTTP 400 under `managed`) | external cluster only, with `--profile=kubeadm-worker` ([kubeadm sysext](bluefin.md#bluefin-hosts-joining-a-kubeadm-cluster)) | yes (node Ignition; `stateDisk` or an install keeps its state) | yes (node Ignition) |

Bluefin's `/usr` has no containerd/kubelet, so under kubeadm it can only be a worker of an external cluster, through the `kubeadm` sysext ([Bluefin hosts joining a kubeadm cluster](bluefin.md#bluefin-hosts-joining-a-kubeadm-cluster)); it ignores `--controlPlaneDisk`: a Bluefin control plane keeps its state on the host's own `stateDisk` or its installed disk. Every host carries a `role` (`control-plane` or `worker`, default worker) set at `POST /register`, through `/hosts` or `/booty.json`, or in the UI host form. The flags are validated at startup; `GET /cluster` reports `{distribution, controlPlane, source, endpoint, apiServer, cni, ready, readyAt, connected, nodes, caFingerprint, hosts:[{mac,hostname,os,role,booted,secureBoot}], warnings}` with no key material. `source` (`managed` or `external`, the same value as `controlPlane`) says whose facts `ready`, `endpoint` and `caFingerprint` are, see [Cluster status](#cluster-status); `connected`, `nodes` and `apiServer` describe the API server Booty itself talks to in either mode. The defaults (`kubeadm`, `external`) equal the join-only behaviour, and a host without a `role` renders byte-identical to before the cluster work, guarded by a golden test.

### Quick start: a managed cluster

kubeadm:

```
booty --controlPlane=managed --controlPlaneEndpoint=10.77.0.10 --controlPlaneDisk=/dev/vdb \
      --containerdDisk=/dev/vda --kubeadmJoin=auto --cni=cilium --serverIP=10.77.0.1
```

k0s:

```
booty --clusterDistribution=k0s --controlPlane=managed --controlPlaneEndpoint=10.77.0.10 \
      --controlPlaneDisk=/dev/vdb --containerdDisk=/dev/vda --cni=none --serverIP=10.77.0.1
```

Then register the hosts, one of them with `role: control-plane` (Flatcar or CoreOS; Bluefin too when the distribution is k0s) and the rest as workers, and power them all on together. `--controlPlaneEndpoint` defaults to the single control-plane host's IP when there is exactly one; reserve it in DHCP or set the flag. What to expect:

1. The control-plane host runs `kubeadm init` (or starts `k0s controller`), installs the CNI and POSTs `/cluster/ready`; `GET /cluster` then shows `ready: true`. That means the API server answers `/readyz` and workers can join, nothing more: watch node and CNI rollout with kubectl.
2. From the control-plane host, `kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes` (kubeadm) or `k0s kubectl get nodes` shows every node `Ready` a few minutes after power-on (measured in QEMU: 7 minutes for a kubeadm CP plus two workers, 7 and 17 minutes for the two k0s mixes).
3. For an admin kubeconfig off the box: kubeadm's is `/etc/kubernetes/admin.conf` on the control-plane host, k0s's is `/var/lib/k0s/pki/admin.conf`. Booty can mint a one-year cluster-admin kubeconfig from its own CA (`AdminKubeconfig` in `pkg/cluster/pki`, used internally for `--kubeadmJoin=auto`), but no endpoint or CLI exposes it yet.

### Disks on PXE-booted nodes

Flatcar and Fedora CoreOS run from RAM when PXE-booted (a tmpfs root of half the memory), so a managed control plane on either **requires `--controlPlaneDisk=/dev/…`**. Booty formats the device **once** (ext4, label `booty-cp`, `wipe_filesystem: false`: an existing `booty-cp` filesystem is reused, a foreign filesystem makes Ignition refuse to boot rather than wipe it), mounts it at `/var/lib/booty-cp` and bind-mounts the state directories from subdirectories of it (`/etc/kubernetes`, `/var/lib/etcd` and `/var/lib/kubelet` under kubeadm; `/var/lib/k0s` under k0s). A `booty-cp-seed.service` copies the Ignition-written PKI onto the disk before the bind mounts with `cp -an`, so the disk copy wins on later boots (under k0s the token-Secret manifest is force-refreshed instead, so a rebooted controller applies what Booty currently renders). Rendering a Flatcar/CoreOS control plane without the flag is refused with HTTP 400 `control-plane host needs --controlPlaneDisk on a PXE-booted OS` plus a warning in `GET /cluster`; `--controlPlaneDisk` and `--containerdDisk` must be different devices (startup error). The disk is the cluster's state: wiping it is a cluster reset.

**Give PXE-booted nodes a `--containerdDisk` too.** A network-booted node's images do not fit its RAM root: in QEMU a 3 GiB worker filled its 1.5 GiB root to 96 % with Cilium's images and went `ImagePullBackOff`, and the control plane's containerd competes with etcd for the same space. With `--containerdDisk` the images live on a disk (ext4, label `ssd`, wiped on every boot: at `/var/lib/containerd` under kubeadm, at `/var/lib/k0s/containerd` on a k0s controller and at `/var/lib/k0s` itself on a k0s worker, whose extracted binaries and kubelet state also live there) and 3 GiB workers come up `Ready`; without it plan on roughly 6 GiB of RAM per node or more. Bluefin k0s nodes install to disk and are exempt from both flags; Bluefin kubeadm workers format `--containerdDisk` ext4 once if it is blank and otherwise mount it as it is ([Bluefin hosts joining a kubeadm cluster](bluefin.md#bluefin-hosts-joining-a-kubeadm-cluster)).

### Managed kubeadm control plane

On the first `managed` start Booty generates the cluster CA, service-account key pair and etcd CA (RSA 2048, 10 years) under `--dataDir/cluster/pki/` (0700, files 0600, never regenerated), or loads a bring-your-own `--clusterCADir` read-only. Next to it, `cluster/tokens.json` (0600) holds a 7-day worker bootstrap token (renewed when under 24 h remain) and the `certificateKey` for `kubeadm init --upload-certs`. Everything a node needs is rendered before any node boots, so the control plane and the workers can be powered on together.

`--controlPlaneEndpoint` (`host[:port]`, port defaults to 6443) is what every node uses for the API server and the one HA hook that ships: point it at a VIP or DNS name later if you add control planes by hand. With exactly one `role: control-plane` host registered it defaults to that host's IP.

**The control-plane host** (`role: control-plane`, `os: flatcar` or `coreos`) gets, on top of the builtin fragment, the same tool chain as a worker (`booty-cni-install` -> `booty-kube-tools` -> `booty-containerd-setup` -> `booty-kubelet-setup`) and then:

| Unit / file | Does |
|---|---|
| `/etc/kubernetes/pki/ca.crt` (0644), `ca.key` (0600) | Booty's cluster CA; kubeadm signs every other certificate with it. **Only** `role: control-plane` hosts ever receive `ca.key`. |
| `/etc/booty/kubeadm-init.yaml` (0600) | v1beta4 `InitConfiguration` (`bootstrapTokens[{token, ttl: 168h}]`, `certificateKey`, `criSocket=unix:///run/containerd/containerd.sock`), `ClusterConfiguration` (`kubernetesVersion=--k8sVersion`, `controlPlaneEndpoint`, `podSubnet=--podCIDR`, `serviceSubnet=--serviceCIDR`, `clusterName=kubernetes`) and a `KubeletConfiguration` (`cgroupDriver: systemd`, `failSwapOn: false`, the worker profile's kubelet settings). Validates with `kubeadm config validate`. |
| `booty-k8s-init.service` (`init.sh`) | `ConditionPathExists=!/etc/kubernetes/kubelet.conf`; loads `br_netfilter`, sets the forwarding sysctls, `kubeadm init --config /etc/booty/kubeadm-init.yaml --upload-certs`. `Restart=on-failure RestartSec=30s`, so a slow image pull just retries. Never reruns on a machine that already initialised. |
| `booty-cluster-ready.service` (`cluster-ready.sh`) | Once `/etc/kubernetes/admin.conf` exists and `/readyz` answers, `POST http://<serverIP>/cluster/ready?mac=<mac>`. Retries every 30 s until it succeeds. |
| `booty-cni-apply.service` (`cni-apply.sh`) | Installs `--cni` with `KUBECONFIG=/etc/kubernetes/admin.conf` (table below), retries every 30 s, and touches `/var/lib/booty-cp/cni-applied` on success so later boots skip it. Absent with `--cni=none`. |

`ready` in `GET /cluster` therefore means "kubeadm init finished on the control-plane host and its API server answers `/readyz`" -- workers can join from that moment. It says nothing about the CNI rollout or about workers being `Ready`; watch `kubectl get nodes` for that (with an external control plane `ready` means something else, see [Cluster status](#cluster-status)). `POST /cluster/ready` is accepted only from a MAC registered as `role: control-plane` (404 otherwise), is idempotent (the first `readyAt` sticks) and is persisted in `--dataDir/cluster/ready.json`.

The control plane keeps kubeadm's default `node-role.kubernetes.io/control-plane:NoSchedule` taint; Booty does not untaint it. Cilium, Calico and flannel tolerate it, so the CNI comes up on a single node, but your own workloads run on workers. Remove the taint yourself if you want a one-node cluster.

**Workers** under `--controlPlane=managed` render today's `kubeadm-worker` profile (implied even without `--profile`) with two changes that also apply in external mode: `booty-k8s-join.service` has `Restart=on-failure RestartSec=30s`, so a worker that boots before the control plane simply retries, and `join.sh` exits 0 when `/etc/kubernetes/kubelet.conf` exists (a disk-installed node that already joined; a PXE worker never has it and re-joins on every boot as before). The join string is `kubeadm join <endpoint>:6443 --token <the persisted bootstrap token> --discovery-token-ca-cert-hash sha256:<from ca.crt>` -- the same token `kubeadm init` was given, so it works before the API exists. With `--kubeadmJoin=auto` Booty first tries to mint a one-hour token with an admin certificate from its own CA and falls back to that pre-generated string while the API is unreachable (logged at debug level, no `X-Booty-Warning`: that is the expected state during bootstrap). A `--joinString`/`--joinStringFile` set explicitly still wins in `static` mode. A `role: control-plane` host never receives worker join units, in either mode.

**Bluefin** hosts cannot run kubeadm (no containerd/kubelet in `/usr`): under `--controlPlane=managed --clusterDistribution=kubeadm` their Ignition endpoints answer HTTP 400 `unsupported distribution kubeadm for os bluefin` and `GET /cluster` warns; in external mode `/ignition/*.json` keeps serving them the plain builtin fragment, and with `--profile=kubeadm-worker` their `bluefin-node.ign` joins them as workers ([Bluefin hosts joining a kubeadm cluster](bluefin.md#bluefin-hosts-joining-a-kubeadm-cluster)). Otherwise Bluefin runs k0s, below.

### Managed k0s

`--clusterDistribution=k0s` makes every `role: control-plane` host a k0s **controller** (`k0s controller --enable-worker`, so it also runs a kubelet) and every other host a k0s **worker**; nothing kubeadm-related is rendered, and `--profile` is refused at startup. k0s is a single static binary that embeds containerd, the kubelet, etcd and kube-proxy, keeps everything under `/var/lib/k0s` and reads its configuration from `/etc/k0s/k0s.yaml`, which is what makes it the one distribution Bluefin Server can run. Whatever the OS, one renderer (`pkg/cluster/k0s`) produces the node's files, units and drop-ins; the Ignition fragment and the Bluefin bundle only differ in how they deliver them.

| File / unit | Controller | Worker |
|---|---|---|
| `/var/lib/k0s/pki/{ca.crt,ca.key,sa.key,sa.pub,etcd/ca.crt,etcd/ca.key}` (0600) | Booty's cluster CA, service-account key pair and etcd CA, in place before k0s's first start so it signs everything else with them. **Only** controllers receive them. | -- |
| `/etc/k0s/k0s.yaml` (0600) | `ClusterConfig` with `spec.api.externalAddress=<endpoint host>` (and `spec.api.port` when the endpoint's port is not 6443), `spec.network.{podCIDR,serviceCIDR}` from `--podCIDR`/`--serviceCIDR` and `spec.network.provider` from `--cni` (table below); everything else is k0s's default. Validates with `k0s config validate`. | -- |
| `/var/lib/k0s/manifests/booty/tokens.yaml` (0600) | The bootstrap-token `Secret`s (worker and controller flavour) k0s applies on start, exactly what `k0s token pre-shared` would have written. | -- |
| `/etc/k0s/token` (0600) | -- | The worker join token (`k0s token pre-shared` format: a gzip+base64 kubeconfig with the CA, `https://<endpoint>:6443` and a bootstrap token). Per boot Booty mints a one-hour token through the API (see [Worker join tokens](#k0s-worker-join-tokens)) and falls back to the pre-shared 7-day token from `cluster/tokens.json` while the controller is not up. With `--controlPlane=external` the token is minted through `--kubeconfig`, falling back to the contents of `--k0sTokenFile`, which must be a *worker* token. |
| `booty-cluster-ready.service` (`cluster-ready.sh`) | Once `/var/lib/k0s/pki/admin.conf` exists and `k0s kubectl get --raw /readyz` answers, `POST /cluster/ready`. Retries every 30 s. | -- |
| `booty-cni-apply.service` (`cni-apply.sh`) | Only with `--cni=cilium` or `flannel` (provider `custom`): the same install scripts as under kubeadm, run through `k0s kubectl` with `KUBECONFIG=/var/lib/k0s/pki/admin.conf`; marker `/var/lib/booty-cp/cni-applied` (Flatcar/CoreOS) or `/var/lib/booty/cni-applied` (Bluefin). | -- |

| `--cni` | `spec.network.provider` | Who installs it |
|---|---|---|
| `none` | `kuberouter` | k0s's built-in default: the cluster is `Ready` out of the box, nothing else runs. Unlike kubeadm, `none` does not leave the node without a network. |
| `calico` | `calico` | k0s's built-in Calico (VXLAN), pod CIDR from `spec.network.podCIDR`; `--cniRelease` has no effect. |
| `cilium` | `custom` | `booty-cni-apply.service`, Cilium pin as in the [CNI table](#cni) (`ipam.mode=kubernetes`; k0s's controller-manager allocates node pod CIDRs). |
| `flannel` | `custom` | `booty-cni-apply.service`, flannel pin as in the [CNI table](#cni). |

**Flatcar and Fedora CoreOS** get, on top of the builtin fragment, `booty-k0s-install.service` (`k0s-install.sh`: downloads the `--k0sVersion` amd64 binary from the [k0s release](https://github.com/k0sproject/k0s/releases) to `/opt/bin/k0s` and verifies its sha256 -- the default `v1.36.4+k0s.0` is pinned in code (`ca1e9e68107335846e8296777fce2ccd654284e6265b4b5d32c34ead872af98f`, checked against the release's `sha256sums.txt` and GitHub's asset digest on 2026-09-26), any other version is verified against that release's `sha256sums.txt`; a binary that already matches is left alone) and then either `k0scontroller.service` (`/opt/bin/k0s controller -c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot`) or `k0sworker.service` (`/opt/bin/k0s worker --token-file /etc/k0s/token`), both shaped like the unit `k0s install` writes (`Restart=always`, `Delegate=yes`, `KillMode=process`) and ordered after the install oneshot and the `/var/lib/k0s` mount. The controller keeps the `node-role.kubernetes.io/master:NoExecute` taint, as k0s itself does for controller+worker nodes.

**Bluefin Server** gets k0s as the opt-in `k0s` sysext (implied for every Bluefin host under `--clusterDistribution=k0s`), which ships `k0scontroller.service` and `k0sworker.service` around its `/usr/bin/k0s`; the image's `k0s-first-boot.service` merges the sysext on every boot and starts `k0sworker.service` when `/etc/k0s/token` exists, `k0scontroller.service` otherwise. Booty therefore adds no k0s unit: the host's [node Ignition](bluefin.md#the-nodes-ignition-config) carries `/etc/k0s/token` on a worker, and on a controller `/etc/sysconfig/k0s` (the controller unit's `EnvironmentFile`) with `K0S_CONTROLLER_ARGS=-c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot` -- replacing the stock `--single`, which is incompatible with joins and etcd -- plus `k0s.yaml`, the PKI, the token-Secret manifest and the `booty-cluster-ready`/`booty-cni-apply` units. A diskless Bluefin controller needs a `stateDisk` to keep `/var/lib/k0s` across reboots.

#### k0s worker join tokens

A k0s join token is a kubeconfig wrapping a Kubernetes bootstrap-token Secret -- the same object `--kubeadmJoin=auto` creates -- so Booty mints k0s worker tokens with the same `Minter`, no flag needed. When a `flatcar`/`coreos` worker fetches `/ignition/builtin.json` (the real child fetch of a boot, not a `preview=1`), or a `bluefin` worker fetches its `bluefin-node.ign` (`HEAD` or `GET`, not a `preview=1`), Booty

1. creates the Secret `kube-system/bootstrap-token-<id>` in the k0s worker shape (`k0s token pre-shared --role worker`, verified against k0s v1.36.4's `pkg/token/manager.go`): `token-id`, `token-secret`, `expiration` = now + `--joinTokenTTL` (default `1h`), `usage-bootstrap-authentication: "true"` and `description: "booty: <hostname> <mac>"`. **No** `usage-bootstrap-signing` and **no** `auth-extra-groups`: k0s binds the implicit `system:bootstrappers` group of every bootstrap token to `system:node-bootstrapper` and the CSR auto-approvers (`pkg/component/controller/systemrbac.yaml`), and the API server reads only the id, secret, usages and expiration -- the description is Booty's cleanup marker;
2. encodes it as `k0s token pre-shared` would (`kubelet-bootstrap` user, `https://<endpoint>:6443`, the cluster CA) and writes it to `/etc/k0s/token`, caching it per MAC for `ttl/2` so the retries of one boot -- and, for Bluefin, the initrd's `HEAD` of `bluefin-node.ign` and the `GET` right after it -- see the same bytes.

Where it talks to the API: with `--controlPlane=managed` it authenticates with an admin client certificate from Booty's own CA (`system:masters`, cluster-admin through the API server's bootstrap RBAC; k0s runs the stock kube-apiserver on `<endpoint>:6443`), embedding Booty's CA in the token; with `--controlPlane=external` it uses `--kubeconfig` (current context; token or client certificate) and embeds that kubeconfig's `certificate-authority(-data)`, the server being `--controlPlaneEndpoint` when set or the kubeconfig's. Controller tokens are not minted (no HA); the controller keeps applying the pre-shared worker and controller Secrets from `tokens.yaml`.

**Fallback.** When the mint fails -- the controller is not up yet, the API is unreachable, no endpoint is known -- the worker gets the pre-shared 7-day token (managed) or the contents of `--k0sTokenFile` (external, re-read per render), logged at debug level with no `X-Booty-Warning`: during bootstrap that is the expected state, and it is what makes a worker powered on together with the controller join at all. Previews never mint: `preview=1` shows the token cached for that MAC or the pre-shared one. A Booty started before the single control-plane host had an IP (no `--controlPlaneEndpoint`) has no minter until it is restarted and keeps serving the pre-shared token, which `GET /cluster` shows through its endpoint warning.

**Cleanup and trust.** Minted k0s tokens carry the `booty:` description, so the same `--updateSchedule` sweep that deletes expired kubeadm tokens deletes them; k0s's own `Worker bootstrap token generated by k0s` Secrets are never touched. What leaks over the boot VLAN is a token worth one hour of node-join, exactly as with `--kubeadmJoin=auto`; the pre-shared 7-day token is still rendered onto the controller's manifests and used by workers only until the controller answers. `--kubeadmJoin`, `--joinString` and the kubeadm join string do nothing under k0s: `{{ .JoinString }}` is only ever the static `--joinString` there.

### External control planes

`--controlPlane=external` (the default) is the join-only path: Booty manages no CA and no control plane, and workers join a cluster you run yourself. For kubeadm that is the [Automatic join tokens](ignition.md#automatic-join-tokens) flow: `--kubeadmJoin=static` with `--joinString`/`--joinStringFile`, or `--kubeadmJoin=auto` minting one-hour tokens in-cluster or from outside the cluster with `--kubeconfig`. For k0s, workers get a one-hour token minted through `--kubeconfig` when set ([k0s worker join tokens](#k0s-worker-join-tokens)), otherwise -- or when minting fails -- their pre-made *worker* join token from `--k0sTokenFile` (re-read per render, so a rotated mounted Secret is picked up), and a `role: control-plane` host renders only the builtin fragment. Under k0s + external, hosts with neither `--kubeconfig` nor `--k0sTokenFile` get the plain builtin fragment, and `--profile=kubeadm-worker` is refused at startup since the distribution is k0s.

### Cluster status

`GET /cluster` (the Overview page's *Cluster* card, polled every 30 s) describes the cluster from whichever side Booty stands on, named by `source`:

| Field | `source: managed` (`--controlPlane=managed`) | `source: external` (the default) |
|---|---|---|
| `ready`, `readyAt` | The control-plane host reported that `kubeadm init` finished and its API server answers `/readyz` (`POST /cluster/ready`); says nothing about the CNI or workers. | `ready` is `connected`: there is no bootstrap to wait for, the cluster is ready when Booty can talk to it. `readyAt` is omitted. |
| `endpoint` | `--controlPlaneEndpoint`, else the single `role: control-plane` host's IP. | `--controlPlaneEndpoint`, else the host:port of the API server Booty talks to (`10.96.0.1:443` in-cluster, the kubeconfig's server otherwise). |
| `caFingerprint` | Booty's own cluster CA (`sha256:` of the DER certificate). | The CA Booty verifies that API server with -- the kubeconfig's `certificate-authority(-data)` or the service account's `ca.crt` -- in the same format, so the two can be compared. |
| `connected`, `nodes`, `apiServer` | The live connection, in both modes: `connected` is whether the API server answered just now, `nodes` how many `GET /api/v1/nodes` listed (0 when unknown), `apiServer` the URL. All false/0/empty when Booty has no client. | |

The card's headline follows: **connected** / **unreachable** for an external control plane, **bootstrapped** / **not ready yet** for a managed one, with the node count and the API server shown when known.

Booty never loads a second configuration for this. With `--autopilot=guard|full` it reuses the autopilot's cluster client and the same cached inspection `GET /autopilot` shows (`cluster.reachable`, `cluster.nodes`; one node listing a minute). With `--autopilot=off` it falls back to the token minter's client (`--kubeadmJoin=auto`, or a k0s minter, or simply running in the cluster) and one `GET /version` a minute -- the endpoint every identity may call -- so `connected`, `endpoint` and `caFingerprint` still work while `nodes` stays 0. A `/cluster` answer therefore costs no API request in the typical case and at most a 3 s probe once a minute; without any client `connected` is false at no cost. When the probe fails, or a reachable server answers with anything but 2xx (RBAC, an expired token), the reason is appended to `warnings` as `API server <url> unreachable: …` or `API server <url>: …`; a managed cluster that has not reported ready yet is expected to be unreachable and gets no such warning.

### CNI

`--cni` picks the plugin `booty-cni-apply.service` installs from the control plane; `--cniRelease` overrides the pinned release (`--cniVersion` keeps meaning containernetworking/plugins). Pins checked 2026-09-25:

| `--cni` | Release | How it is installed |
|---|---|---|
| `cilium` (default) | [v1.20.2](https://github.com/cilium/cilium/releases/tag/v1.20.2) via [cilium-cli v0.20.1](https://github.com/cilium/cilium-cli/releases/tag/v0.20.1) (tarball sha256-verified against the published `.sha256sum`, unpacked to `/opt/bin/cilium`) | `cilium install --version v1.20.2 --set ipam.mode=kubernetes` (pod ranges come from kubeadm's node podCIDRs), then `cilium status --wait`. `--cniRelease` changes the Cilium version, not the CLI pin. |
| `calico` | [v3.32.2](https://github.com/projectcalico/calico/releases/tag/v3.32.2) | `kubectl apply --server-side` of `manifests/tigera-operator.yaml`, then an `Installation` with `ipPools[0].cidr=--podCIDR` (VXLANCrossSubnet, NAT outgoing) and the `APIServer` CR; waits for `calico-node`. Under k0s the built-in Calico provider is used instead (see the k0s table above). |
| `flannel` | [v0.28.9](https://github.com/flannel-io/flannel/releases/tag/v0.28.9) | Downloads `kube-flannel.yml` from the release; when `--podCIDR` is not flannel's built-in `10.244.0.0/16` the script rewrites the single `"Network"` entry in `net-conf.json` and refuses (unit fails, retries) if the manifest no longer has exactly one. Then `kubectl apply` and waits for `kube-flannel-ds`. |
| `none` | -- | kubeadm: nothing is installed; bring your own. k0s: provider `kuberouter`, Ready out of the box. |

### Trust model and the cluster CA

With `--controlPlane=managed`, `--dataDir/cluster/` holds the cluster CA and its private key, which is cluster-admin: whoever can read that directory (or the node Booty renders it onto) owns the cluster. Booty never serves it over `/data/`, `/config` or `/cluster`, but the control-plane host's Ignition (`/ignition/builtin.json?mac=<cp>`) necessarily carries `ca.key`, the bootstrap token and the certificate key, readable by anyone on the boot VLAN during that host's first boot. Keep the data directory as private as your kubeconfig, VLAN-isolate the control plane's first boot, or use `--controlPlane=external` and keep the CA elsewhere. The UI masks `PRIVATE KEY` blocks in preview panes; the API does not. See [Trust model](trust-model.md#trust-model) for the rest of Booty's threat model.
