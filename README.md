# Booty

A simple iPXE server for booting Flatcar-Linux, Fedora CoreOS (including [Universal Blue](https://universal-blue.org) images) and [Bluefin Server](https://github.com/projectbluefin/server) on BIOS and x86-64 UEFI machines.

## Features

* iPXE boot (BIOS and x86-64 UEFI) into the latest Flatcar-Linux or CoreOS, and UEFI HTTP Boot of Bluefin Server's signed netboot UKI (diskless, or installed to disk)
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux, CoreOS and the Bluefin Server releases
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Configuration view (`/config`: every effective setting with its source, secrets redacted) and a Butane template editor with Butane validation, read-only aware for GitOps/ConfigMap mounts (see [Configuration view and editor](docs/configuration.md#configuration-view-and-editor))
* Builtin Ignition fragment (hostname, SSH keys, update timer, install-complete callback, health report) merged into every host's config -- your Butane only carries what is specific to your fleet
* Fleet status: every host reports what it is running and whether a reboot is pending (`/update-check`, `/info`), and what it looks like after boot (`/health`: failed units, journal errors, hardware)
* Autopilot: `--autopilot=guard` gates every reboot into a new release (kernel/Ignition fetched, OS up, no failed units, node Ready with its workloads healthy within 15 min), retries once, rolls the host back to `lastGood` and holds the fleet there when the release is at fault; `full` adds the Bluefin canary-serial rollout, TIMEOUT/retry, quarantine and skip-to-next. With `--autopilotSoak`/`--autopilotCooldown` the rollouts are paced: `canary: true` hosts still follow every release, and the rest of the fleet moves in waves, one per cooldown, onto releases that soaked healthy on a canary. Reboots go through kured, SSH or the Kubernetes API (see [Autopilot](docs/autopilot.md#autopilot))
* `--profile=kubeadm-worker`: the whole Kubernetes worker setup (CNI, kubeadm/kubelet/kubectl/crictl, kubelet units, `kubeadm join` on every boot) as a versioned, embedded Ignition fragment
* `--kubeadmJoin=auto`: a fresh, one-hour kubeadm bootstrap token per boot, minted through the Kubernetes API -- no long-lived token on the boot network
* `--controlPlane=managed`: Booty generates the cluster CA and renders a whole kubeadm control plane (persistent state on `--controlPlaneDisk`, CNI installed from it) so a cluster boots from bare metal with nothing but Booty (see [Cluster bootstrap](docs/cluster.md#cluster-bootstrap))
* `--clusterDistribution=k0s`: the same with k0s -- a controller and workers on Flatcar, Fedora CoreOS **and Bluefin Server**, which gets `k0s` as its opt-in sysext and its role through its node Ignition; workers get a one-hour join token per boot, minted like the kubeadm ones (see [Managed k0s](docs/cluster.md#managed-k0s))
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered), or are registered automatically with `--autoRegister` (see [Auto-registration](docs/booting.md#auto-registration))
* `booty init` scaffolds a data directory with a commented starter Butane template and prints the DHCP settings
* Support for different operating systems and ignition files per machine
* Bluefin Server: diskless boots over UEFI HTTP Boot with a per-host Ignition config (hostname, a `core` user with SSH keys and sudo, a persistent `/var` disk, opt-in zfs/kubestellar/k0s/NVIDIA sysexts), optional unattended install to disk, releases from GitHub or an OCI artifact with optional signature checking (see [Bluefin Server](docs/bluefin.md#bluefin-server))
* **EXPERIMENTAL**: Support for per-ostree images per machine (in conjunction with [ignition rebase scripts](examples/bazzite.but)); the host rebases straight from its registry, Booty only hands the reference to the template
* When "Install" is set to Y, it auto-flips to N once the host fetches its Ignition config (i.e. the installer has started)
* A **Storage** page and `GET /storage`: what the data directory holds per OS and release, why each release is kept, and how to free space (see [Storage](docs/storage.md#storage))
* Self-contained binary: the iPXE bootloaders (`undionly.kpxe`, `ipxe.efi`, `snponly.efi`) and the Web UI are embedded, so it starts without network access
* `--secureBoot`: UEFI HTTP Boot for machines with Secure Boot enabled -- a Microsoft-signed iPXE shim handed out over ProxyDHCP, the signed artefacts synced and pinned, the Flatcar Secure Boot CA extracted for enrolment (see [Secure Boot](docs/secure-boot.md#secure-boot-uefi-http-boot))
* `/healthz` for liveness/readiness probes; graceful shutdown on SIGTERM

## Quick start

```
booty init ./data          # writes data/config/ignition.yaml (commented starter Butane) and data/hardware.json
booty --dataDir ./data     # --serverIP is autodetected; pass it explicitly behind a VIP/NAT
```

`booty init [dir]` (default: `--dataDir`, `BOOTY_DATADIR` or `./data`) never overwrites existing files -- rerunning it prints `exists, skipped` -- and ends with the DHCP settings for your network (`next-server`, `filename undionly.kpxe` / `ipxe.efi`), the run command and the UI URL. The starter template is valid on its own: hostname, SSH keys, the update timer and the booted callback come from the [builtin fragment](docs/ignition.md#composition), so edit it only for what is specific to your fleet.

### Container

```
docker run --rm -it \
  --network=host \
  --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  -v $PWD/data:/data/ \
  ghcr.io/jeefy/booty:v1.1.0 \
  --serverIP=192.168.1.10
```

The image runs as **nonroot** (uid 65532; applies to `:main` and v1.1.0 and later) with the `NET_BIND_SERVICE` and `NET_RAW` file capabilities on the binary, so the two `--cap-add` flags are required to keep them in the bounding set, and the `/data` volume must be writable by uid 65532 (`chown -R 65532:65532 data`, or `fsGroup: 65532` plus `runAsUser: 65532` on Kubernetes). This also applies to **Podman**: its default capability set lacks `NET_RAW`, and with the setcap `+ep` binary exec fails with `EPERM` unless `NET_RAW` is in the bounding set, so Podman users must pass `--cap-add NET_RAW --cap-add NET_BIND_SERVICE` just like the docker example above. See [Trust model and capabilities](docs/trust-model.md#running-as-root--capabilities).

### Kubernetes

[Example deployment](examples/k8s.yaml): Booty in `kube-system` on the control-plane node with `hostNetwork`, `--profile=kubeadm-worker --kubeadmJoin=auto`, the ServiceAccount and Roles the token minting needs, a ConfigMap with the site-only Butane, a Secret with the SSH keys mounted for `--sshAuthorizedKeysFile`, `/healthz` probes, `runAsNonRoot` with `NET_BIND_SERVICE`+`NET_RAW` only, and two MetalLB Services sharing one VIP. Set `--serverIP` to the address booting machines actually reach.

More runbooks: [Examples and development](docs/development.md#examples).

### Releases

Container tags: `ghcr.io/jeefy/booty:vX.Y.Z` tags are built by CI from the
tagged commit with SLSA provenance and a keyless cosign signature. Verify a
release with:

```
COSIGN_EXPERIMENTAL=1 cosign verify-attestation --new-bundle-format=false \
  --certificate-identity-regexp=".*" --certificate-oidc-issuer-regexp=".*" \
  --type slsaprovenance ghcr.io/jeefy/booty@<digest>
```

The `:main` tag tracks the default branch. Release-candidate tags
(`-rc.N`) do **not** move `:latest`. Base images are pinned by digest.
Notable changes between releases are listed in [CHANGELOG.md](CHANGELOG.md);
release notes with upgrade steps live under `docs/releases/`
([v1.0.0](docs/releases/v1.0.0.md), [v1.1.0](docs/releases/v1.1.0.md)).

## Flags

Every flag can be set on the command line or through the environment as `BOOTY_<FLAGNAME>` (upper-cased, e.g. `BOOTY_SERVERIP=192.168.1.10`, `BOOTY_PROFILE=kubeadm-worker`). The full reference -- the complete `--help` output, startup validation rules and the endpoint-override flags (`--githubAPIURL`, `--fedoraMirrorURL`, ...) -- is in [docs/configuration.md](docs/configuration.md). The settings you will touch first:

| Flag | Default | What it is |
|---|---|---|
| `--dataDir` | `/data` | Directory to store stateful data |
| `--serverIP` | autodetected | IP address clients connect to; set explicitly behind a VIP/NAT |
| `--serverHttpPort` | `--httpPort` | HTTP port clients use when it differs from the listen port |
| `--httpPort` | `8080` | Port of the HTTP server |
| `--tftpPort` | `69` | UDP port of the TFTP server |
| `--flatcarChannel` / `--coreOSChannel` | `stable` | Release channel to track, or `none` to disable the OS |
| `--flatcarVersion` / `--bluefinVersion` | newest | Pin one release instead of tracking the channel |
| `--autoRegister` | off | Register unknown MACs on first boot as this OS |
| `--profile` | none | `kubeadm-worker` appends the Kubernetes worker setup |
| `--kubeadmJoin` | `static` | `auto` mints a one-hour bootstrap token per boot |
| `--controlPlane` | `external` | `managed` makes Booty render the control plane itself |
| `--clusterDistribution` | `kubeadm` | `kubeadm` or `k0s` |
| `--autopilot` | `off` | `guard` gates upgrades, `full` adds the serial Bluefin rollout |
| `--proxyDHCP` | off | Answer PXE clients alongside the existing DHCP server |
| `--secureBoot` | off | Answer UEFI HTTP Boot clients with the signed iPXE shim |

`--serverIP` is the address booting machines use to reach Booty. When left empty it is autodetected at startup from the default route (logged as `serverIP autodetected`). Set it explicitly whenever that address is not what clients should use -- behind a MetalLB/keepalived VIP, a NAT or a hostPort mapping the node IP is the wrong answer. `--serverHttpPort` only matters when clients reach Booty on a different port than it listens on (port mapping); it defaults to `--httpPort`, and `:80` is omitted from generated URLs.

## Documentation

* [Booting: DHCP, iPXE and ProxyDHCP](docs/booting.md) -- bootloaders, the boot sequence, auto-registration, zero-touch ProxyDHCP
* [Ignition: composition, profiles and join tokens](docs/ignition.md) -- the builtin fragment, the kubeadm worker profile, per-boot tokens
* [Cluster bootstrap (kubeadm and k0s)](docs/cluster.md) -- managed and external control planes, disks, CNI, cluster status
* [Fleet status and autopilot](docs/autopilot.md) -- update checks, health gates, rollouts, soak/cooldown, actuators, reports
* [Bluefin Server](docs/bluefin.md) -- releases, boot paths, per-host Ignition, kubeadm joins, NVIDIA, installing to disk
* [Secure Boot (UEFI HTTP Boot)](docs/secure-boot.md) -- the signed iPXE shim, trusted CAs, enrolment, artefacts
* [Storage](docs/storage.md) -- release layout, retention and pruning, the Storage page
* [Power](docs/power.md) -- power tracking, Wake-on-LAN, reboot and shutdown
* [Configuration](docs/configuration.md) -- full flag/environment reference and the config view and editor
* [Trust model and capabilities](docs/trust-model.md) -- no authentication by design; the capabilities the server needs
* [Examples and development](docs/development.md) -- example configs, building, testing, CI

## Additional thoughts

**Why?**

I like treating (most of) my machines like cattle. This is an easier and more lightweight way to tackle network booting and patch management.

**Can you make it do X?**

Feature requests / optimizations / PRs are welcome! Feel free to ping me [@jeefy](https://twitter.com/jeefy) on Twitter.
