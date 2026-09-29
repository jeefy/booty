# Booty

A simple iPXE server for booting Flatcar-Linux, Fedora CoreOS (including [Universal Blue](https://universal-blue.org) images) and [Bluefin Server](https://github.com/projectbluefin/server) on BIOS and x86-64 UEFI machines.

```
> booty --help
Easy iPXE server for Flatcar, CoreOS, Bluefin Server, and more

Usage:
  booty [flags]
  booty [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  init        Create a data directory with a starter Butane template and an empty hardware map
  node-reboot Reboot the host this runs on: sync, then SIGRTMIN+5 to PID 1 (systemd reboot.target)

Flags:
      --autoRegister string                Register unknown MACs on their first /booty.ipxe or /ignition.json fetch (bluefin: their first UEFI HTTP Boot netboot UKI fetch, which ProxyDHCP then offers every unknown x86-64 HTTP Boot client) as this OS (flatcar, coreos or bluefin) instead of sending them to the brig; empty disables
      --autopilot string                   Self-healing upgrades: 'off', 'guard' (health gate, retry, downgrade and fleet hold for every OS) or 'full' (guard plus the Bluefin canary-serial rollout). P2: anything but off is a dry run that connects to the cluster (in-cluster or --kubeconfig), detects kured and reports the actuator it would use in GET /autopilot; nothing reboots (default "off")
      --autopilotDrainTimeout duration     How long the API actuator keeps retrying evictions refused by a PodDisruptionBudget before it gives up on draining a node (default 10m0s)
      --autopilotImage string              Image of the reboot Pods (must be Booty's own, it runs 'booty node-reboot'); defaults to the BOOTY_IMAGE environment variable, and the API actuator refuses to run without one
      --autopilotNamespace string          Namespace the autopilot's API actuator creates reboot Pods in; defaults to Booty's own (the POD_NAMESPACE downward-API variable), else kube-system
      --bluefinKeyring string              OpenPGP public keyring (binary as for gpgv --keyring, or armored) that must have signed a Bluefin release's SHA256SUMS (SHA256SUMS.gpg); the sync fails closed when set. Empty trusts SHA256SUMS from the release as-is
      --bluefinOCI string                  Fetch the Bluefin Server release files from this ORAS OCI artifact repository instead of GitHub releases (e.g. ghcr.io/projectbluefin/bluefin-server; tags <version> and latest; http://host:port/repo for a plain-HTTP registry). --githubToken is sent to ghcr.io only
      --bluefinRepo string                 GitHub repository whose v<version> releases provide the Bluefin Server netboot UKI, OS DDI and sysexts (default "projectbluefin/server")
      --bluefinVersion string              Pin a specific Bluefin Server release (e.g. 20260927.123, tag v20260927.123). When empty, tracks the newest v<version> release (or the OCI artifact's latest tag)
      --builtin string                     Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys, health), or 'none' to serve the user config as-is (default "hostname,update,booted,sshkeys,health")
      --clusterCADir string                Bring-your-own cluster CA directory for --controlPlane=managed (kubeadm: ca.crt/ca.key; k0s: also sa.key, sa.pub, etcd/ca.crt, etcd/ca.key), read-only; empty generates one under --dataDir/cluster/pki
      --clusterDistribution string         Kubernetes distribution of the cluster Booty provisions: 'kubeadm' or 'k0s' (Bluefin hosts: k0s, or kubeadm workers of an external cluster) (default "kubeadm")
      --cni string                         Network plugin installed from the first control plane: 'cilium', 'calico', 'flannel' or 'none' (default "cilium")
      --cniRelease string                  Overrides the pinned release of the selected --cni (--cniVersion keeps meaning containernetworking/plugins)
      --cniVersion string                  containernetworking/plugins release installed by the kubeadm-worker profile (default "v1.1.1")
      --containerdDisk string              Block device PXE-booted nodes format (ext4, wiped on every boot) for their image cache: /var/lib/containerd under kubeadm, /var/lib/k0s on a k0s worker and /var/lib/k0s/containerd on a k0s controller, e.g. /dev/sda; Bluefin kubeadm workers never format it, they mount the existing ext4 at /var/lib/containerd-disk and keep containerd in its bluefin/ directory; empty keeps it on the root filesystem
      --controlPlane string                Who runs the control plane: 'external' (join-only, today's behaviour) or 'managed' (Booty generates the cluster CA under --dataDir/cluster/ and renders the role: control-plane host) (default "external")
      --controlPlaneDisk string            Block device the managed control plane formats once (ext4, label booty-cp, never wiped) and keeps its state on (kubeadm: /etc/kubernetes, /var/lib/etcd, /var/lib/kubelet; k0s: /var/lib/k0s), e.g. /dev/vda; required for a role: control-plane host on PXE-booted Flatcar/CoreOS (Bluefin installs to disk) and must differ from --containerdDisk
      --controlPlaneEndpoint string        host[:port] every node uses for the API server (VIP or DNS name for HA); with --controlPlane=managed it defaults to the single role: control-plane host's IP
      --coreOSArchitecture string          Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string               CoreOS channel to look for updates (default "stable")
      --crictlVersion string               cri-tools release installed by the kubeadm-worker profile; defaults to the --k8sVersion minor with patch 0 (cri-tools tags once per minor, e.g. v1.34.0)
      --dataDir string                     Directory to store stateful data (default "/data")
      --debug                              Enable debug logging
      --doInstallClearOn string            When to clear a host's pending doInstall: 'ignition' (first Ignition fetch; Bluefin: the bluefin-node.ign carrying the install unit), 'booted' (only on POST /booted from the installed system; Bluefin behaves like 'next-boot') or 'next-boot' (Bluefin: the first netboot UKI fetch at least --installMinDuration after the install boot was served; other OSes behave like 'booted'). A Bluefin host whose doInstall clears becomes mode installed (default "ignition")
      --efiBootloader string               iPXE build ProxyDHCP hands x86-64 UEFI clients: 'ipxe' (ipxe.efi, iPXE's own NIC drivers) or 'snponly' (snponly.efi, the firmware's network stack); HTTP Boot clients get the matching signed shim (ipxe-shimx64.efi / snponly-shimx64.efi) (default "ipxe")
      --fedoraGrubVersion string           Fedora grub2-efi-x64 package version (e.g. 2.12-64.fc44) whose grubx64.efi is served next to the Fedora shim; sha256 pinned in code for the default (default "2.12-64.fc44")
      --fedoraShimVersion string           Fedora shim-x64 package version (e.g. 16.1-7) whose shimx64.efi Secure-Boot CoreOS hosts chain through; sha256 pinned in code for the default (default "16.1-7")
      --flatcarArchitecture string         Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string              Flatcar channel to look for updates (default "stable")
      --flatcarVersion string              Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel
      --githubToken string                 GitHub token sent as a bearer token to the releases API (raises the unauthenticated 60 requests/hour limit); no scopes needed
  -h, --help                               help for booty
      --hostnameTemplate string            Go template for auto-registered hostnames; fields: .MAC, .MACSuffix (last 3 bytes hex), .MACFlat (12 hex), .IP (default "node-{{ .MACSuffix }}")
      --httpPort int                       Port to use for the HTTP server (default 8080)
      --installMinDuration duration        Minimum time between a Bluefin install boot and the netboot UKI fetch that counts as 'install finished' for --doInstallClearOn=next-boot/booted; earlier fetches install again (default 3m0s)
      --joinString string                  The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)
      --joinStringFile string              File holding the kubeadm join string (e.g. a mounted Secret); re-read on every render and wins over --joinString
      --joinTokenTTL duration              Lifetime of bootstrap tokens minted through the Kubernetes API (--kubeadmJoin=auto, k0s worker tokens); expired ones are deleted on the --updateSchedule tick (default 1h0m0s)
      --k0sTokenFile string                File holding a pre-made k0s worker join token for an external k0s control plane; the fallback when --kubeconfig is not set or minting fails
      --k0sVersion string                  k0s release Flatcar/CoreOS hosts download to /opt/bin/k0s under --clusterDistribution=k0s (sha256-verified; the default is pinned in code and matches Bluefin Server's /usr/bin/k0s, other versions are checked against the release's sha256sums.txt) (default "v1.36.4+k0s.0")
      --k8sVersion string                  Kubernetes release installed by the kubeadm-worker profile (default "v1.34.3")
      --kubeadmJoin string                 Where the kubeadm join string comes from: 'static' (--joinString/--joinStringFile) or 'auto' (mint a short-lived bootstrap token through the Kubernetes API on every boot: in-cluster, via --kubeconfig, or with Booty's own CA when --controlPlane=managed) (default "static")
      --kubeconfig string                  Kubeconfig for talking to an external control plane from outside the cluster: minting join tokens (kubeadm with --kubeadmJoin=auto, k0s worker tokens always) and the autopilot's cluster client
      --kubeletUnitsURL string             Base URL the kubeadm-worker profile fetches kubelet/kubelet.service and kubeadm/10-kubeadm.conf from (pin or mirror it) (default "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest")
      --ociGC                              Delete unreferenced OCI blobs from the local registry after a fully successful image sync (default true)
      --ociGCEmpty                         Allow blob GC to wipe the whole OCI blob cache when no registered host references an ostree image
      --podCIDR string                     Pod network CIDR of the cluster (default "10.244.0.0/16")
      --profile string                     Node profile appended to the builtin Ignition fragment for flatcar/coreos hosts: '' or 'kubeadm-worker' (CNI plugins, kubeadm/kubelet/kubectl/crictl, kubelet units, kubeadm join on every boot); bluefin workers get the same join through the kubeadm sysext in their node Ignition
      --proxyDHCP                          EXPERIMENTAL: answer PXE clients as a ProxyDHCP server (UDP 67 + 4011) so the network's DHCP server needs no next-server/filename
      --proxyDHCPListen string             IP or interface name the ProxyDHCP server binds to (default all interfaces)
      --proxyDHCPRelay                     Answer relayed PXE requests (giaddr set) on the ProxyDHCP server
      --rebootSSHKey string                Private key for the SSH actuator (root on Bluefin, core with sudo on Flatcar/CoreOS; host keys pinned on first use in --dataDir/autopilot/known_hosts); empty disables it
      --secureBoot                         Answer UEFI HTTP Boot clients (Secure Boot firmware) over ProxyDHCP with a Microsoft-signed iPXE shim and sync the signed boot artefacts into --dataDir/secureboot/; requires --proxyDHCP. Off: HTTP Boot clients other than Bluefin hosts (always offered their netboot UKI) are ignored and nothing is downloaded
      --secureBootIPXEShimVersion string   ipxe/shim release providing the Microsoft-signed ipxe-shimx64.efi (sha256 pinned in code for the default, the release's asset digest otherwise) (default "ipxe-16.1")
      --secureBootIPXEVersion string       ipxe/ipxe release whose ipxeboot.tar.gz provides the iPXE-CA-signed x86_64-sb/ipxe.efi and snponly.efi (sha256 pinned in code for the default) (default "v2.0.0")
      --secureBootTrusted string           Comma separated Secure Boot CAs the fleet's firmware db trusts besides the implied 'microsoft': 'flatcar' asserts the Flatcar CA (served at /boot/secureboot/flatcar-ca.der) is enrolled so Flatcar kernels may be booted under Secure Boot
      --serverHttpPort int                 HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort
      --serverIP string                    IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)
      --serviceCIDR string                 Service network CIDR of the cluster (default "10.96.0.0/12")
      --sshAuthorizedKeys strings          SSH public key added to the 'core' user by the sshkeys builtin (repeatable)
      --sshAuthorizedKeysFile string       File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin
      --tftpBlockSize int                  TFTP block size to negotiate with clients (default 1468)
      --tftpPort int                       UDP port to use for the TFTP server (default 69)
      --updateSchedule string              Cron schedule for the Flatcar/CoreOS/Bluefin version checks and OSTree image sync (default "*/5 * * * *")
      --webDir string                      Directory with the built Web UI, used when no UI is embedded in the binary (default "./web/dist")

Use "booty [command] --help" for more information about a command.
```

Every flag can also be set through the environment as `BOOTY_<FLAGNAME>` (upper-cased, e.g. `BOOTY_SERVERIP=192.168.1.10`, `BOOTY_JOINSTRING=...`, `BOOTY_PROFILE=kubeadm-worker`). The older `IGNITION_FILE`, `HARDWARE_MAP` and `FLATCAR_VERSION_PIN` names still work.

`--serverIP` is the address booting machines use to reach Booty. When left empty it is autodetected at startup from the default route (logged as `serverIP autodetected`). Set it explicitly whenever that address is not what clients should use -- behind a MetalLB/keepalived VIP, a NAT or a hostPort mapping the node IP is the wrong answer. `--serverHttpPort` only matters when clients reach Booty on a different port than it listens on (port mapping); it defaults to `--httpPort`, and `:80` is omitted from generated URLs.

## Quick start

```
booty init ./data          # writes data/config/ignition.yaml (commented starter Butane) and data/hardware.json
booty --dataDir ./data     # --serverIP is autodetected; pass it explicitly behind a VIP/NAT
```

`booty init [dir]` (default: `--dataDir`, `BOOTY_DATADIR` or `./data`) never overwrites existing files -- rerunning it prints `exists, skipped` -- and ends with the DHCP settings for your network (`next-server`, `filename undionly.kpxe` / `ipxe.efi`), the run command and the UI URL. The starter template is valid on its own: hostname, SSH keys, the update timer and the booted callback come from the [builtin fragment](#composition), so edit it only for what is specific to your fleet.

## Features

* iPXE boot (BIOS and x86-64 UEFI) into the latest Flatcar-Linux or CoreOS, and UEFI HTTP Boot of Bluefin Server's signed netboot UKI (diskless, or installed to disk)
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux, CoreOS and the Bluefin Server releases
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Configuration view (`/config`: every effective setting with its source, secrets redacted) and a Butane template editor with Butane validation, read-only aware for GitOps/ConfigMap mounts (see [Configuration view and editor](#configuration-view-and-editor))
* Builtin Ignition fragment (hostname, SSH keys, update timer, install-complete callback, health report) merged into every host's config -- your Butane only carries what is specific to your fleet
* Fleet status: every host reports what it is running and whether a reboot is pending (`/update-check`, `/info`), and what it looks like after boot (`/health`: failed units, journal errors, hardware)
* Autopilot groundwork: releases are kept in `data/<os>/<version>/` behind `current`/`previous`/`lastGood` links for every OS, and a per-host `targetVersion` picks which one a machine boots and is asked to reboot into (see [Autopilot](#autopilot))
* `--profile=kubeadm-worker`: the whole Kubernetes worker setup (CNI, kubeadm/kubelet/kubectl/crictl, kubelet units, `kubeadm join` on every boot) as a versioned, embedded Ignition fragment
* `--kubeadmJoin=auto`: a fresh, one-hour kubeadm bootstrap token per boot, minted through the Kubernetes API -- no long-lived token on the boot network
* `--controlPlane=managed`: Booty generates the cluster CA and renders a whole kubeadm control plane (persistent state on `--controlPlaneDisk`, CNI installed from it) so a cluster boots from bare metal with nothing but Booty (see [Cluster bootstrap](#cluster-bootstrap))
* `--clusterDistribution=k0s`: the same with k0s -- a controller and workers on Flatcar, Fedora CoreOS **and Bluefin Server**, which gets `k0s` as its opt-in sysext and its role through its node Ignition; workers get a one-hour join token per boot, minted like the kubeadm ones (see [Managed k0s](#managed-k0s))
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered), or are registered automatically with `--autoRegister` (see [Auto-registration](#auto-registration))
* `booty init` scaffolds a data directory with a commented starter Butane template and prints the DHCP settings
* Support for different operating systems and ignition files per machine
* Bluefin Server: diskless boots over UEFI HTTP Boot with a per-host Ignition config (hostname, SSH keys, a persistent `/var` disk, opt-in zfs/kubestellar/k0s sysexts), optional unattended install to disk, releases from GitHub or an OCI artifact with optional signature checking (see [Bluefin Server](#bluefin-server))
* **EXPERIMENTAL**: Support for per-ostree images per machine (in conjunction with [ignition rebase scripts](examples/bazzite.but))
  * Auto-caches OCI images used for hosts (and has a page listing cached artifacts)
  * When "Install" is set to Y, it auto-flips to N once the host fetches its Ignition config (i.e. the installer has started)
* Self-contained binary: the iPXE bootloaders (`undionly.kpxe`, `ipxe.efi`, `snponly.efi`) and the Web UI are embedded, so it starts without network access
* `--secureBoot`: UEFI HTTP Boot for machines with Secure Boot enabled -- a Microsoft-signed iPXE shim handed out over ProxyDHCP, the signed artefacts synced and pinned, the Flatcar Secure Boot CA extracted for enrolment (see [Secure Boot](#secure-boot-uefi-http-boot))
* `/healthz` for liveness/readiness probes; graceful shutdown on SIGTERM

## Booting: DHCP and the iPXE bootloaders

Booty ships three iPXE bootloaders, built from a pinned upstream release (see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and [`boot/VERSION`](boot/VERSION)) and embedded in the binary. They are served at the root of the TFTP namespace and over HTTP as `GET /boot/<name>`, and are also copied into `--dataDir` on start-up if missing (so `/data/<name>` keeps working for anyone who pointed DHCP there):

| DHCP client architecture (option 93) | Firmware | `filename` |
|---|---|---|
| `00:00` | BIOS / legacy PC | `undionly.kpxe` |
| `00:07`, `00:09` | x86-64 UEFI | `ipxe.efi` (or `snponly.efi`) |

`ipxe.efi` carries iPXE's own network drivers and works on most hardware; `snponly.efi` only talks to the firmware's UEFI network stack (SNP), so prefer it on machines whose NIC misbehaves with `ipxe.efi` (no link, hangs after "Initialising devices", or firmware that refuses to hand over the NIC) -- it is smaller and boots through whatever driver the firmware already brought up. Legacy pxelinux/syslinux booting is not supported; only iPXE.

All three carry the same embedded script ([`boot/embed.ipxe`](boot/embed.ipxe)): it runs DHCP (retrying every 5 s until it gets a lease), then chains `tftp://${next-server}/booty.ipxe`, falls back to `http://${next-server}/booty.ipxe?mac=${mac}` and, if neither is reachable, prints a message and drops to the iPXE shell. `${next-server}` is the DHCP `next-server`/`siaddr`, i.e. Booty, so no `user-class = "iPXE"` chainload-loop trickery is needed in the DHCP config: the bootloader never asks for the DHCP `filename` again.

Point your DHCP server at Booty:

**ISC dhcpd**

```
option architecture-type code 93 = unsigned integer 16;

subnet 192.168.1.0 netmask 255.255.255.0 {
  range 192.168.1.100 192.168.1.200;
  next-server 192.168.1.10;                      # Booty
  if option architecture-type = 00:00 {
    filename "undionly.kpxe";
  } elsif option architecture-type = 00:07 or option architecture-type = 00:09 {
    filename "ipxe.efi";                         # or "snponly.efi"
  }
}
```

**dnsmasq**

```
dhcp-match=set:efi-x86_64,option:client-arch,7
dhcp-match=set:efi-x86_64,option:client-arch,9
dhcp-boot=tag:efi-x86_64,ipxe.efi,,192.168.1.10
dhcp-boot=undionly.kpxe,,192.168.1.10
```

**UniFi / UDM** -- the Network application only allows one "Network Boot" filename per network, so pick the bootloader matching your fleet: `undionly.kpxe` for BIOS machines or `ipxe.efi` for UEFI machines, with the "Network Boot server" set to Booty's IP. Mixed fleets need a DHCP server that can branch on option 93 (above), or a second network.

If Booty listens on a non-standard TFTP port (`--tftpPort`), UEFI firmware cannot fetch the bootloader from it -- run a TFTP relay or serve the bootloader from another TFTP server (`GET /boot/<name>` gives you the exact bytes); the embedded script still reaches Booty over HTTP through `--serverHttpPort`.

### How a host boots

1. DHCP hands the machine `next-server` = Booty and `filename` = `undionly.kpxe` / `ipxe.efi` as above; the firmware fetches it over TFTP.
2. The embedded script chains `booty.ipxe` over TFTP. That file is only a stub that chains to `http://<serverIP>/booty.ipxe?mac=${mac}` -- iPXE fills in its own MAC, so identification does not depend on ARP working across routers.
3. `/booty.ipxe` looks the MAC up in the hardware database and renders the boot script for that host's OS (`flatcar`, `coreos` or `bluefin`). Unregistered hosts get an interactive menu (boot from disk / reboot) and show up under "Unknown hosts" in the UI so you can register them with one click -- unless `--autoRegister` is set, in which case they are registered on the spot (see [Auto-registration](#auto-registration)).
4. The OS fetches `http://<serverIP>/ignition.json?mac=<mac>`. For a registered host that is a tiny wrapper whose `ignition.config.merge` points at two children: Booty's builtin fragment (`/ignition/builtin.json`) and the host's Butane template rendered to Ignition (`/ignition/user.json`; variables: `.Hostname`, `.ServerIP`, `.JoinString`, `.OSTreeImage`). Ignition fetches and merges them itself, later entries winning, so your template overrides the builtin (and the `--profile` appended to it). The wrapper fetch records `booted`/`ip` for the host and, by default, clears a pending `doInstall`; the child fetches have no side effects. With `--doInstallClearOn=booted` the flag instead stays set until the installed system calls `POST http://<serverIP>/booted?mac=<mac>` (the builtin `booty-booted.service` does exactly that), so a failed install keeps the host in install mode. `--doInstallClearOn=next-boot` is meant for Bluefin Server; see [Installing to disk](#installing-to-disk). Add `&preview=1` (the UI does) to look at a config without recording a boot. Unregistered hosts receive an Ignition config whose only unit reboots the machine (the "brig").
5. Kernel/initrd/rootfs are served from `/data/`. Every OS keeps its releases in `data/<os>/<version>/` (`flatcar/4757.2.0/`, `coreos/44.20260913.2.1/`, `bluefin/26.09.673/`) behind relative `current`, `previous` and `lastGood` symlinks, and the boot scripts name the release directory the host boots (its `targetVersion`, else `current`), so the kernel and initrd always come from the same release and updates are atomic. The pre-release-directory names (`data/flatcar_production_pxe.vmlinuz`, `data/fedora-coreos-<version>-live-*`) stay as symlinks into `<os>/current` for anything that still uses them; see [Releases and retention](#releases-and-retention).

Bluefin Server hosts boot a signed UKI instead and fetch their own Ignition config from `/bluefin/<mac>/`: straight from the firmware over UEFI HTTP Boot, or, with Secure Boot off, chainloaded by `/booty.ipxe` in step 3; on legacy BIOS step 3 boots the UKI's kernel and initrd instead (diskless only). See [How a Bluefin host boots](#how-a-bluefin-host-boots), [Chainloading from iPXE](#chainloading-from-ipxe-secure-boot-off) and [Legacy BIOS diskless boot](#legacy-bios-diskless-boot).

`/booty.ipxe` and `/ignition.json` fall back to an ARP lookup of the requesting IP when called without `?mac=`; the shipped scripts always pass it.

### Auto-registration

By default an unknown MAC gets the menu and the brig until you register it (in the UI, or `POST /register`). With `--autoRegister=flatcar|coreos|bluefin` Booty instead registers the host itself the first time it fetches `/booty.ipxe` or `/ignition.json`: it is stored with that OS, the requesting IP and a hostname rendered from `--hostnameTemplate`, logged as `Auto-registered unknown host`, and immediately served the real boot script and Ignition -- the machine never sees the brig and never appears under "Unknown hosts". With `--autoRegister=bluefin` that happens on the first netboot UKI fetch (`/bluefin/<mac>/*.efi`) instead, and ProxyDHCP offers the UKI to every unknown x86-64 UEFI HTTP Boot client; other values never register through `/bluefin/`. Only the boot path does this; `/hosts`, `/ignition/user.json`, `/ignition/builtin.json` and `/update-check` still answer 404 for unregistered MACs and never create hosts.

`--hostnameTemplate` is a Go `text/template` (default `node-{{ .MACSuffix }}`) with these fields:

| Field | Example for `aa:bb:cc:dd:ee:ff` from `192.168.1.57` |
|---|---|
| `.MAC` | `aa:bb:cc:dd:ee:ff` (not a valid hostname on its own) |
| `.MACSuffix` | `ddeeff` |
| `.MACFlat` | `aabbccddeeff` |
| `.IP` | `192.168.1.57` |

The result must be an RFC 1123 label or dotted name (`[a-z0-9-]`, no leading/trailing hyphen, at most 253 characters); Booty checks at startup that the template parses and renders a valid name for a dummy MAC, and rejects the flag otherwise. `/register` applies the same rule to a non-empty `hostname`. Hostnames are not required to be unique -- a template like `worker` registers every machine as `worker` with only a warning in the log -- so keep a MAC-derived field in it.

**Trust model caveat.** Auto-registration turns "any device on the boot VLAN can PXE boot" into "any device on the boot VLAN becomes a registered host that receives your Ignition config" (including `--joinString` and the builtin SSH keys, which unregistered hosts can already read; see [Trust model](#trust-model)). For a kubeadm cluster this also means a stray or wrongly named machine joins as a Node; a hostname collision creates a duplicate Node object or hijacks an existing one. The brig is the safer default; turn auto-registration on when the boot network is closed and you want zero-touch provisioning. Auto-registered hosts start with `doInstall` unset, so for CoreOS and Bluefin installs you still flip it in the UI.

## Composition

Every Booty deployment used to hand-write the same Butane boilerplate: `/etc/hostname` from `{{ .Hostname }}`, an update timer plus a `version-check.sh` that polls `/version.txt` and touches `/var/run/reboot-required` for [kured](https://github.com/kubereboot/kured), and the `booty-booted.service` callback. Booty now provides those itself and composes them with your config using Ignition's own merge mechanism.

`GET /ignition.json?mac=<mac>` for a registered host returns:

```json
{"ignition":{"version":"3.4.0","config":{"merge":[
  {"source":"http://192.168.1.10/ignition/builtin.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A01"},
  {"source":"http://192.168.1.10/ignition/user.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A01"}]}}}
```

* `ignition.version` is taken from your rendered config, so the wrapper never asks for a newer spec than your template does.
* Ignition merges the children **in order, later entries winning**: files are keyed by path, units by name, users by name, list fields append. Anything in your template with the same path/name overrides the builtin, e.g. define your own `booty-update.timer` to change the schedule.
* `/ignition/builtin.json` (spec 3.4.0, understood by Flatcar >= 3760 and Fedora CoreOS) carries, selectable with `--builtin` (default `hostname,update,booted,sshkeys,health`; `none` disables composition and `/ignition.json` serves your config verbatim, exactly as before):
  * `hostname` -- `/etc/hostname` (0644) with the hostname from the hardware database. Skipped when the host has no hostname.
  * `sshkeys` -- `passwd.users[core].sshAuthorizedKeys` from `--sshAuthorizedKeysFile` (one key per line, `#` comments ignored) and/or repeated `--sshAuthorizedKeys`. Skipped when there are no keys.
  * `booted` -- `booty-booted.service` (oneshot, enabled) that POSTs `/booted?mac=<mac>` once the installed system is up.
  * `update` -- `/opt/booty/update-check` (0755, inline, nothing fetched over HTTP at run time) plus `booty-update.service` and `booty-update.timer` (`OnCalendar=*:0/10`, `RandomizedDelaySec=60`). The script reads `/etc/os-release` (and `rpm-ostree status` on OSTree systems), calls `GET /update-check`, and touches or removes `/var/run/reboot-required` for kured. It fails closed: when Booty is unreachable or answers with anything but `rebootRequired:true|false`, the reboot flag is left alone.
  * `health` -- `/opt/booty/health-report` (0755, inline) plus `booty-health.service`: a oneshot ordered `After=multi-user.target` (and `booty-booted.service`), `RemainAfterExit=yes`, retried every 30 s on failure, that POSTs the node's health to `/health?mac=<mac>` once the system is up. The script is plain bash over `systemctl`, `journalctl`, `uname` and `curl` (no `jq`; it escapes the JSON itself) and sends `running` (`OSTREE_VERSION`, else `VERSION_ID`, from `/etc/os-release`), `failedUnits` (`systemctl list-units --state=failed`), `journalErrors` (the last 50 lines of `journalctl -p err -b`, each at most 300 bytes, at most 8 KiB in total), `dmi` (`sys_vendor`, `product_name`, `bios_version` and the root-readable `product_uuid` from `/sys/class/dmi/id`, no serial numbers; the UUID identifies the machine and stays out of reports), `firmware` (`uefi` when `/sys/firmware/efi` exists, else `bios`), `kernel` (`uname -r`) and `bootID`. See [Autopilot](#autopilot) for what Booty does with it.
* `/ignition/user.json` is your Butane template rendered and translated to Ignition -- the same bytes `/ignition.json` used to return. When neither `--ignitionFile` nor a per-host `ignitionFile` is set and `config/ignition.yaml` does not exist, Booty renders an embedded minimal template (hostname only) so a fresh data directory boots without any Butane at all. A per-host `ignitionFile` that is missing is still a 500 (that is a configuration error).
* Previews (`preview=1`, no side effects): `part=user` and `part=builtin` return the children, `part=merged` returns a pretty-printed **server-side** merge (both children upconverted to spec 3.5, builtin as parent, user as child) so you can see what the node will end up with. This is display-only; the node always performs its own merge.

## kubeadm worker profile

`--profile=kubeadm-worker` turns a registered `flatcar` or `coreos` host into a stateless Kubernetes worker without any Kubernetes-specific Butane. Booty appends the profile to `/ignition/builtin.json` after the builtin pieces, so the merge order on the node is **builtin -> profile -> user** and your template still overrides anything by path or unit name. `bluefin` hosts do not get this fragment: Bluefin workers join through their node Ignition and the `kubeadm` sysext instead, see [Bluefin hosts joining a kubeadm cluster](#bluefin-hosts-joining-a-kubeadm-cluster).

What the profile installs, all as inline files under `/opt/booty/` (0755, embedded as `data:` URLs -- nothing is fetched from Booty at run time) and a chain of enabled oneshot units, each `Requires=`/`After=` the previous one. Every script that calls kubeadm/kubectl puts `/opt/bin` first on `PATH`:

| Unit | Script | Does |
|---|---|---|
| `booty-cni-install.service` (`Wants=network-online.target`) | `cni.sh` | Unpacks [containernetworking/plugins](https://github.com/containernetworking/plugins) `--cniVersion` into `/opt/bin/` |
| `booty-kube-tools.service` | `kube-tools.sh` | Downloads the `--k8sVersion` `kubeadm`/`kubelet`/`kubectl` static binaries from `dl.k8s.io` and `crictl` `--crictlVersion` (default = `--k8sVersion` minor) from [cri-tools](https://github.com/kubernetes-sigs/cri-tools) into `/opt/bin/` and creates `/etc/kubernetes/manifests`. The same on **Flatcar** and **Fedora CoreOS**: a PXE-booted FCOS runs from a read-only `erofs` `/usr` where neither `dnf` nor `rpm-ostree` can layer packages, so the former `pkgs.k8s.io` RPM path is gone; `/opt` (`/var/opt` on FCOS) is writable on both. |
| `booty-containerd-setup.service` | `containerd.sh` | Makes sure a containerd with the **systemd cgroup driver** is running before the kubelet. **Flatcar** already runs one (`--config /usr/share/containerd/config.toml`, `SystemdCgroup = true`): the script reads the config path from the unit's `CONTAINERD_CONFIG=` environment, sees the setting and does nothing. **Fedora CoreOS** ships `/usr/bin/containerd` with `containerd.service` disabled and a stock `/etc/containerd/config.toml` without it: the stock file is kept as `config.toml.booty-orig`, `containerd config default` (version 3) regenerates it, `SystemdCgroup = false` is flipped by a `sed` that refuses to run (unit fails, visible in `journalctl`) unless the generated file has exactly one such line, then `systemctl enable containerd` + `restart`. Either way it writes `/etc/crictl.yaml` pointing at `unix:///run/containerd/containerd.sock`. |
| `booty-kubelet-setup.service` | `systemd.sh` | Fetches `kubelet/kubelet.service` and `kubeadm/10-kubeadm.conf` from `--kubeletUnitsURL` (default: the `master` templates in [kubernetes/release](https://github.com/kubernetes/release/tree/master/cmd/krel/templates/latest); pin or mirror it if you want reproducible boots), rewrites `/usr/bin` to `/opt/bin` (so both `ExecStart=` lines run the absolute `/opt/bin/kubelet`), sets `KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false`, enables and starts the kubelet |
| `booty-k8s-join.service` (`Restart=on-failure RestartSec=30s`) | `join.sh` | `Environment="JOIN_STRING=..."`; exits 0 when `/etc/kubernetes/kubelet.conf` already exists (a node that joined earlier and kept its disk), otherwise loads `br_netfilter`, sets the bridge/forwarding sysctls, runs `kubeadm reset -f` and then the join string. A failed join is retried every 30 s; an empty `JOIN_STRING` logs a message and exits 0, so the node still boots. |

`--containerdDisk=/dev/sda` additionally adds an ext4 filesystem (`wipe_filesystem: true`, label `ssd`) mounted at `/var/lib/containerd` through `var-lib-containerd.mount` and a `containerd.service` drop-in that `Requires=`/`After=` the mount -- an ephemeral image cache that is wiped on every boot together with the rest of the stateless worker.

Flags: `--profile` (`""` or `kubeadm-worker`, validated at start-up), `--k8sVersion` (default `v1.34.3`), `--cniVersion` (default `v1.1.1`), `--crictlVersion` (default `--k8sVersion`), `--containerdDisk` (default none), `--kubeletUnitsURL`. `part=merged` previews show the profile merged with your template.

## Automatic join tokens

`--kubeadmJoin` decides where `{{ .JoinString }}` and the profile's `JOIN_STRING` come from:

* `static` (default): `--joinString`, or the trimmed contents of `--joinStringFile` when set (re-read on every render, so a rotated mounted Secret is picked up; the file wins over the flag).
* `auto`: Booty mints a **bootstrap token per boot** through the Kubernetes API it runs in. When a registered `flatcar`/`coreos` host fetches `/ignition.json` (the real fetch, not `preview=1`), Booty
  1. reads `kube-public/cluster-info` (cached 10 minutes) for the **external** API endpoint (`clusters[0].cluster.server`) and the CA, and computes the discovery hash `sha256(SubjectPublicKeyInfo)`;
  2. creates the Secret `kube-system/bootstrap-token-<id>` (type `bootstrap.kubernetes.io/token`, 6-char id + 16-char secret from `crypto/rand`, `usage-bootstrap-authentication`/`-signing`, `auth-extra-groups: system:bootstrappers:kubeadm:default-node-token`, `expiration` = now + `--joinTokenTTL` (default `1h`), `description: "booty: <hostname> <mac>"`);
  3. renders `kubeadm join <endpoint> --token <id>.<secret> --discovery-token-ca-cert-hash sha256:<hex>` and caches it per MAC for `ttl/2`. The `builtin.json`/`user.json` children Ignition fetches seconds later reuse the cached value (and mint if the cache is cold, e.g. after a Booty restart); retries within a boot do not churn tokens; previews only ever show the cache.

  Booty talks to the API server in-cluster by default: `KUBERNETES_SERVICE_HOST`/`_PORT`, the service account token from `/var/run/secrets/kubernetes.io/serviceaccount/token` (re-read per request, it rotates) and the CA from `.../ca.crt`; 10 s timeouts; no client-go, plain `net/http`. Outside the cluster, `--kubeconfig` points it at a kubeconfig (current context; token or client certificate), and with `--controlPlane=managed` it authenticates with an admin certificate from Booty's own CA (see [Cluster bootstrap](#cluster-bootstrap)). RBAC needed (see [examples/k8s.yaml](examples/k8s.yaml)): a Role in `kube-system` on `secrets` with `create`, `list`, `delete`, `get`, and a Role in `kube-public` on `configmaps` named `cluster-info` with `get`.

  **Cleanup**: on every `--updateSchedule` tick Booty lists `kube-system` Secrets of type `bootstrap.kubernetes.io/token` and deletes the ones whose `description` starts with `booty:` and whose `expiration` has passed (kubeadm and k0s flavours alike). Tokens it did not create are never touched.

  **Failure mode**: if minting fails (RBAC, API down, not running in a cluster) Booty logs an error, sets `X-Booty-Warning: kubeadm join token unavailable` on the `/ignition.json` response and still serves the config with an empty `JOIN_STRING`. The node boots but does not join; fix the cause and reboot it.

## Cluster bootstrap

Booty provisions a whole Kubernetes cluster from PXE: it generates the cluster CA, renders a control plane onto a registered host and joins every other host as a worker, on Flatcar, Fedora CoreOS and Bluefin Server. You pick the distribution per cluster with `--clusterDistribution` (`kubeadm`, the default, or `k0s`) and decide who runs the control plane with `--controlPlane`: `managed` means Booty renders the control plane itself, `external` (the default) is the join-only behaviour from the previous sections, pointing at a cluster you already have. The design and the QEMU evidence for every supported combination are in [docs/plans/2026-09-26-cluster-bootstrap.md](docs/plans/2026-09-26-cluster-bootstrap.md).

| OS | kubeadm control plane | kubeadm worker | k0s control plane | k0s worker |
|---|---|---|---|---|
| Flatcar | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) |
| Fedora CoreOS | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) | yes, needs `--controlPlaneDisk` | yes (`--containerdDisk` recommended) |
| Bluefin Server | not supported (HTTP 400 under `managed`) | external cluster only, with `--profile=kubeadm-worker` ([kubeadm sysext](#bluefin-hosts-joining-a-kubeadm-cluster)) | yes (node Ignition; `stateDisk` or an install keeps its state) | yes (node Ignition) |

Bluefin's `/usr` has no containerd/kubelet, so under kubeadm it can only be a worker of an external cluster, through the `kubeadm` sysext ([Bluefin hosts joining a kubeadm cluster](#bluefin-hosts-joining-a-kubeadm-cluster)); it ignores `--controlPlaneDisk`: a Bluefin control plane keeps its state on the host's own `stateDisk` or its installed disk. Every host carries a `role` (`control-plane` or `worker`, default worker) set at `POST /register`, through `/hosts` or `/booty.json`, or in the UI host form. The flags are validated at startup; `GET /cluster` reports `{distribution, controlPlane, endpoint, cni, ready, readyAt, caFingerprint, hosts:[{mac,hostname,os,role,booted}], warnings}` with no key material. The defaults (`kubeadm`, `external`) equal the join-only behaviour, and a host without a `role` renders byte-identical to before the cluster work, guarded by a golden test.

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

**Give PXE-booted nodes a `--containerdDisk` too.** A network-booted node's images do not fit its RAM root: in QEMU a 3 GiB worker filled its 1.5 GiB root to 96 % with Cilium's images and went `ImagePullBackOff`, and the control plane's containerd competes with etcd for the same space. With `--containerdDisk` the images live on a disk (ext4, label `ssd`, wiped on every boot: at `/var/lib/containerd` under kubeadm, at `/var/lib/k0s/containerd` on a k0s controller and at `/var/lib/k0s` itself on a k0s worker, whose extracted binaries and kubelet state also live there) and 3 GiB workers come up `Ready`; without it plan on roughly 6 GiB of RAM per node or more. Bluefin k0s nodes install to disk and are exempt from both flags; Bluefin kubeadm workers mount `--containerdDisk` without formatting it ([Bluefin hosts joining a kubeadm cluster](#bluefin-hosts-joining-a-kubeadm-cluster)).

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

`ready` in `GET /cluster` therefore means "kubeadm init finished on the control-plane host and its API server answers `/readyz`" -- workers can join from that moment. It says nothing about the CNI rollout or about workers being `Ready`; watch `kubectl get nodes` for that. `POST /cluster/ready` is accepted only from a MAC registered as `role: control-plane` (404 otherwise), is idempotent (the first `readyAt` sticks) and is persisted in `--dataDir/cluster/ready.json`.

The control plane keeps kubeadm's default `node-role.kubernetes.io/control-plane:NoSchedule` taint; Booty does not untaint it. Cilium, Calico and flannel tolerate it, so the CNI comes up on a single node, but your own workloads run on workers. Remove the taint yourself if you want a one-node cluster.

**Workers** under `--controlPlane=managed` render today's `kubeadm-worker` profile (implied even without `--profile`) with two changes that also apply in external mode: `booty-k8s-join.service` has `Restart=on-failure RestartSec=30s`, so a worker that boots before the control plane simply retries, and `join.sh` exits 0 when `/etc/kubernetes/kubelet.conf` exists (a disk-installed node that already joined; a PXE worker never has it and re-joins on every boot as before). The join string is `kubeadm join <endpoint>:6443 --token <the persisted bootstrap token> --discovery-token-ca-cert-hash sha256:<from ca.crt>` -- the same token `kubeadm init` was given, so it works before the API exists. With `--kubeadmJoin=auto` Booty first tries to mint a one-hour token with an admin certificate from its own CA and falls back to that pre-generated string while the API is unreachable (logged at debug level, no `X-Booty-Warning`: that is the expected state during bootstrap). A `--joinString`/`--joinStringFile` set explicitly still wins in `static` mode. A `role: control-plane` host never receives worker join units, in either mode.

**Bluefin** hosts cannot run kubeadm (no containerd/kubelet in `/usr`): under `--controlPlane=managed --clusterDistribution=kubeadm` their Ignition endpoints answer HTTP 400 `unsupported distribution kubeadm for os bluefin` and `GET /cluster` warns; in external mode `/ignition/*.json` keeps serving them the plain builtin fragment, and with `--profile=kubeadm-worker` their `bluefin-node.ign` joins them as workers ([Bluefin hosts joining a kubeadm cluster](#bluefin-hosts-joining-a-kubeadm-cluster)). Otherwise Bluefin runs k0s, below.

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

**Bluefin Server** gets k0s as the opt-in `k0s` sysext (implied for every Bluefin host under `--clusterDistribution=k0s`), which ships `k0scontroller.service` and `k0sworker.service` around its `/usr/bin/k0s`; the image's `k0s-first-boot.service` merges the sysext on every boot and starts `k0sworker.service` when `/etc/k0s/token` exists, `k0scontroller.service` otherwise. Booty therefore adds no k0s unit: the host's [node Ignition](#the-nodes-ignition-config) carries `/etc/k0s/token` on a worker, and on a controller `/etc/sysconfig/k0s` (the controller unit's `EnvironmentFile`) with `K0S_CONTROLLER_ARGS=-c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot` -- replacing the stock `--single`, which is incompatible with joins and etcd -- plus `k0s.yaml`, the PKI, the token-Secret manifest and the `booty-cluster-ready`/`booty-cni-apply` units. A diskless Bluefin controller needs a `stateDisk` to keep `/var/lib/k0s` across reboots.

#### k0s worker join tokens

A k0s join token is a kubeconfig wrapping a Kubernetes bootstrap-token Secret -- the same object `--kubeadmJoin=auto` creates -- so Booty mints k0s worker tokens with the same `Minter`, no flag needed. When a `flatcar`/`coreos` worker fetches `/ignition/builtin.json` (the real child fetch of a boot, not a `preview=1`), or a `bluefin` worker fetches its `bluefin-node.ign` (`HEAD` or `GET`, not a `preview=1`), Booty

1. creates the Secret `kube-system/bootstrap-token-<id>` in the k0s worker shape (`k0s token pre-shared --role worker`, verified against k0s v1.36.4's `pkg/token/manager.go`): `token-id`, `token-secret`, `expiration` = now + `--joinTokenTTL` (default `1h`), `usage-bootstrap-authentication: "true"` and `description: "booty: <hostname> <mac>"`. **No** `usage-bootstrap-signing` and **no** `auth-extra-groups`: k0s binds the implicit `system:bootstrappers` group of every bootstrap token to `system:node-bootstrapper` and the CSR auto-approvers (`pkg/component/controller/systemrbac.yaml`), and the API server reads only the id, secret, usages and expiration -- the description is Booty's cleanup marker;
2. encodes it as `k0s token pre-shared` would (`kubelet-bootstrap` user, `https://<endpoint>:6443`, the cluster CA) and writes it to `/etc/k0s/token`, caching it per MAC for `ttl/2` so the retries of one boot -- and, for Bluefin, the initrd's `HEAD` of `bluefin-node.ign` and the `GET` right after it -- see the same bytes.

Where it talks to the API: with `--controlPlane=managed` it authenticates with an admin client certificate from Booty's own CA (`system:masters`, cluster-admin through the API server's bootstrap RBAC; k0s runs the stock kube-apiserver on `<endpoint>:6443`), embedding Booty's CA in the token; with `--controlPlane=external` it uses `--kubeconfig` (current context; token or client certificate) and embeds that kubeconfig's `certificate-authority(-data)`, the server being `--controlPlaneEndpoint` when set or the kubeconfig's. Controller tokens are not minted (no HA); the controller keeps applying the pre-shared worker and controller Secrets from `tokens.yaml`.

**Fallback.** When the mint fails -- the controller is not up yet, the API is unreachable, no endpoint is known -- the worker gets the pre-shared 7-day token (managed) or the contents of `--k0sTokenFile` (external, re-read per render), logged at debug level with no `X-Booty-Warning`: during bootstrap that is the expected state, and it is what makes a worker powered on together with the controller join at all. Previews never mint: `preview=1` shows the token cached for that MAC or the pre-shared one. A Booty started before the single control-plane host had an IP (no `--controlPlaneEndpoint`) has no minter until it is restarted and keeps serving the pre-shared token, which `GET /cluster` shows through its endpoint warning.

**Cleanup and trust.** Minted k0s tokens carry the `booty:` description, so the same `--updateSchedule` sweep that deletes expired kubeadm tokens deletes them; k0s's own `Worker bootstrap token generated by k0s` Secrets are never touched. What leaks over the boot VLAN is a token worth one hour of node-join, exactly as with `--kubeadmJoin=auto`; the pre-shared 7-day token is still rendered onto the controller's manifests and used by workers only until the controller answers. `--kubeadmJoin`, `--joinString` and the kubeadm join string do nothing under k0s: `{{ .JoinString }}` is only ever the static `--joinString` there.

### External control planes

`--controlPlane=external` (the default) is the join-only path: Booty manages no CA and no control plane, and workers join a cluster you run yourself. For kubeadm that is the [Automatic join tokens](#automatic-join-tokens) flow: `--kubeadmJoin=static` with `--joinString`/`--joinStringFile`, or `--kubeadmJoin=auto` minting one-hour tokens in-cluster or from outside the cluster with `--kubeconfig`. For k0s, workers get a one-hour token minted through `--kubeconfig` when set ([k0s worker join tokens](#k0s-worker-join-tokens)), otherwise -- or when minting fails -- their pre-made *worker* join token from `--k0sTokenFile` (re-read per render, so a rotated mounted Secret is picked up), and a `role: control-plane` host renders only the builtin fragment. Under k0s + external, hosts with neither `--kubeconfig` nor `--k0sTokenFile` get the plain builtin fragment, and `--profile=kubeadm-worker` is refused at startup since the distribution is k0s.

### CNI

`--cni` picks the plugin `booty-cni-apply.service` installs from the control plane; `--cniRelease` overrides the pinned release (`--cniVersion` keeps meaning containernetworking/plugins). Pins checked 2026-09-25:

| `--cni` | Release | How it is installed |
|---|---|---|
| `cilium` (default) | [v1.20.2](https://github.com/cilium/cilium/releases/tag/v1.20.2) via [cilium-cli v0.20.1](https://github.com/cilium/cilium-cli/releases/tag/v0.20.1) (tarball sha256-verified against the published `.sha256sum`, unpacked to `/opt/bin/cilium`) | `cilium install --version v1.20.2 --set ipam.mode=kubernetes` (pod ranges come from kubeadm's node podCIDRs), then `cilium status --wait`. `--cniRelease` changes the Cilium version, not the CLI pin. |
| `calico` | [v3.32.2](https://github.com/projectcalico/calico/releases/tag/v3.32.2) | `kubectl apply --server-side` of `manifests/tigera-operator.yaml`, then an `Installation` with `ipPools[0].cidr=--podCIDR` (VXLANCrossSubnet, NAT outgoing) and the `APIServer` CR; waits for `calico-node`. Under k0s the built-in Calico provider is used instead (see the k0s table above). |
| `flannel` | [v0.28.9](https://github.com/flannel-io/flannel/releases/tag/v0.28.9) | Downloads `kube-flannel.yml` from the release; when `--podCIDR` is not flannel's built-in `10.244.0.0/16` the script rewrites the single `"Network"` entry in `net-conf.json` and refuses (unit fails, retries) if the manifest no longer has exactly one. Then `kubectl apply` and waits for `kube-flannel-ds`. |
| `none` | -- | kubeadm: nothing is installed; bring your own. k0s: provider `kuberouter`, Ready out of the box. |

### Trust model and the cluster CA

With `--controlPlane=managed`, `--dataDir/cluster/` holds the cluster CA and its private key, which is cluster-admin: whoever can read that directory (or the node Booty renders it onto) owns the cluster. Booty never serves it over `/data/`, `/config` or `/cluster`, but the control-plane host's Ignition (`/ignition/builtin.json?mac=<cp>`) necessarily carries `ca.key`, the bootstrap token and the certificate key, readable by anyone on the boot VLAN during that host's first boot. Keep the data directory as private as your kubeconfig, VLAN-isolate the control plane's first boot, or use `--controlPlane=external` and keep the CA elsewhere. The UI masks `PRIVATE KEY` blocks in preview panes; the API does not. See [Trust model](#trust-model) for the rest of Booty's threat model.

## Fleet status

`GET /update-check?mac=&os=&version=&image=&digest=` is what `booty-update-check` calls every 10 minutes. It answers `{"rebootRequired":bool,"running":"...","target":"...","reason":"..."}`, and the rule is the same for every OS: **`rebootRequired` when the host's effective target release differs from what it reports running**, where the effective target is the host's `targetVersion` when set, else the fleet target (the OS's `current` release). Booty answers `false` while no release of that OS is cached or the host reported no version, so without a `targetVersion` the Flatcar and CoreOS answers are exactly what they were before per-host targets existed:

* Flatcar (`os=flatcar`, from `/etc/os-release`): compares the reported `VERSION_ID` with the target release (`flatcar <running> differs from served <target>`).
* OSTree systems (`os=coreos`, including Universal Blue images): `rpm-ostree status` supplies the running image reference and digest. If the host's registered `ostreeImage` is cached in Booty's registry, `rebootRequired` is set when the cached digest differs from the running one. Plain Fedora CoreOS without an image compares `OSTREE_VERSION` with the target CoreOS release.
* Bluefin Server (`os=bluefin` or `bluefin-server`, or a host registered as `bluefin`): compares `VERSION_ID` with the target release; a diskless host behind it gets `rebootRequired:true` with the reason `bluefin: re-image on reboot into <target>`, since a reboot re-images it from Booty into that release -- that is the whole upgrade and rollback mechanism. (Installed Bluefin hosts run no Booty Ignition and never ask.)
* Booty never says `true` when it cannot determine the target; 400/404 answers make the client leave the reboot flag alone.

Each check is recorded on the host (`running`, `lastCheck`, `rebootPending`, visible in `/booty.json`, `/hosts` and the UI), rewritten at most once a minute when nothing changed. `GET /info` gains `"fleet":{"hosts":N,"pendingReboots":N}` so you can see at a glance how many nodes are waiting on kured.

## Autopilot

Booty is growing into a self-healing upgrade controller: roll a release across the fleet, check that each node comes back healthy, and downgrade to the last good release when it does not. The design, state machine and remaining slices are in [docs/plans/2026-09-28-autopilot.md](docs/plans/2026-09-28-autopilot.md). What ships today is **P1, the signals and the versions**, and **P2, cluster awareness and the actuators** behind a dry run -- nothing acts on them yet:

### Health reports

`POST /health?mac=<mac>` is what the `health` builtin's `booty-health.service` calls once a node reaches `multi-user.target` (on Flatcar, CoreOS and diskless Bluefin alike; see [Composition](#composition) for the body). Registered hosts only (404 otherwise); the body is capped at 64 KiB and every field is trimmed and bounded server-side (at most 200 failed units, 50 journal lines of 300 bytes within 8 KiB, `firmware` must be `uefi` or `bios`). The report is stored as the host's `health` field with a `receivedAt` stamp and shows in `/hosts`, `/booty.json` and the UI:

```json
"health":{"receivedAt":"2026-09-28T12:00:00Z","bootID":"5b1c…","running":"26.09.673",
          "failedUnits":["booty-kubeadm-join.service"],"journalErrors":["2026-09-28T10:00:01+0000 kernel: …"],
          "dmi":{"vendor":"HP","product":"HP EliteDesk 800 G1 DM","biosVersion":"L01 v02.78"},
          "firmware":"uefi","kernel":"6.17.1-300.fc44.x86_64"}
```

A report replaces the previous one; a repeated POST from the same boot (same `bootID`) converges on the same stored state and is logged at debug level only. It sets the host's `running` when the update check has not yet. Journal lines may carry hostnames and addresses, so Booty logs them at debug level only -- the info-level log has counts, the release, the firmware and the kernel.

### Cluster awareness and actuators

`--autopilot=guard|full` (default `off`) turns on the groundwork the controller of the next slice will drive. In P2 it is a **dry run**: Booty connects to the cluster, works out how it *would* reboot a host, logs that at start-up and answers `GET /autopilot`; nothing reboots, cordons or evicts. `off` skips all of it (`/autopilot` answers `{"mode":"off","actuator":"none","dryRun":true}`).

**Cluster client.** The same plain `net/http` client the token minter uses (in-cluster service account, or `--kubeconfig`; `--controlPlane=managed` shares the minter's admin certificate), extended to read nodes (Ready condition and its transition time, `spec.unschedulable`, the control-plane label, `nodeInfo`: `systemUUID`, kubelet, OS image, kernel, runtime, kured's `weave.works/kured-*` annotations), the pods on a node across all namespaces (phase, ready/restart counts, controlling owner, mirror pods) and the DaemonSets. Failures are typed: unreachable, forbidden (RBAC), not found. On it sits the **health-gate primitive** the controller will poll every 30 s: a node is healthy when it is `Ready`, every pod on it that is not a Job's is `Running` with all containers ready (or `Succeeded`), and no container restarted since the baseline sample taken before the reboot.

**Actuators**, chosen in this order each time one is needed:

1. **kured** -- a DaemonSet named `kured`, or labelled `app=kured` / `app.kubernetes.io/name=kured`, in any namespace (detection cached 5 min). Booty does nothing itself: `/update-check` already answers `rebootRequired:true` while the host's target differs from what it runs, the node's `booty-update.timer` touches `/var/run/reboot-required`, and kured drains, reboots and uncordons. Booty never touches kured's lock.
2. **Kubernetes API** -- cordon (`PATCH nodes/<n>` `spec.unschedulable`), evict every pod that is neither a DaemonSet's nor a mirror pod through `pods/eviction` (PodDisruptionBudget refusals are retried with their `Retry-After` up to `--autopilotDrainTimeout`, default 10 m), then a **reboot Pod** on the node: Booty's own image (`--autopilotImage`, default the `BOOTY_IMAGE` environment variable; refused with a clear error when unknown), `hostPID: true`, privileged, `nodeName` pinned, tolerating every taint, `restartPolicy: Never`, `activeDeadlineSeconds: 120`, named `booty-node-reboot-<node>` (the previous one is deleted first), in `--autopilotNamespace` (default Booty's own from `POD_NAMESPACE`, else `kube-system`). It runs **`booty node-reboot`**: `sync`, then `SIGRTMIN+5` to the host's PID 1, which makes systemd start `reboot.target`. Uncordon once the node passes the health gate.
3. **SSH** -- `--rebootSSHKey=<private key>`: `systemctl reboot` as `root` on Bluefin, `sudo systemctl reboot` as `core` on Flatcar/CoreOS. Host keys are pinned on first use into `--dataDir/autopilot/known_hosts` and must match afterwards. The drain goes through the API when a client exists, else there is none.

The node name is the host's `hostname` (both OS paths set it from Booty); when the host's health report carries a DMI `productUUID` (the `health` builtin now sends `/sys/class/dmi/id/product_uuid`, root-readable, stored in `health.dmi.productUUID`) it must equal the node's `nodeInfo.systemUUID`, or the actuator refuses. Like the MAC, it is an identifier and never enters a report.

**RBAC** for the API paths ([examples/k8s.yaml](examples/k8s.yaml)): a `ClusterRole` with `nodes` get/list/watch/patch, `pods` get/list/watch, `pods/eviction` create and `apps/daemonsets` get/list/watch, and a `Role` in Booty's namespace with `pods` create/delete/get for the reboot Pods. The Deployment also sets `POD_NAMESPACE` (downward API) and `BOOTY_IMAGE` (a literal; keep it equal to the container image, the downward API cannot supply it).

`GET /autopilot` in P2:

```json
{"mode":"guard","cluster":{"reachable":true,"kured":true,"nodes":6,"apiServer":"https://..."},"actuator":"kured","dryRun":true}
```

`cluster.reachable` is whether the node list succeeded (a 403 still counts as reachable and carries the RBAC error in `cluster.error`; a dead server does not), `actuator` is `kured`, `api`, `ssh` or `none`, and `sshUsers` names the per-OS logins when it is `ssh`. The only code that builds an eviction or a reboot Pod is behind the actuator methods; P2's wiring never calls them (the test suite asserts the endpoint issues nothing but GETs against a fake API server).

### Releases and retention

Every OS keeps its releases in `data/<os>/<version>/` behind three relative symlinks: `current` (the fleet target, repointed atomically once a release is fully on disk and verified), `previous` (the release `current` replaced, so hosts mid-boot can finish) and `lastGood` (the newest release the whole fleet was healthy on). P1 initialises `lastGood` to `current` once and leaves it there; the controller of a later slice moves it. **Pruning keeps exactly the releases `current`, `previous`, `lastGood` and any registered host's `targetVersion` name**, and removes every other release directory -- so a bad release can be rolled back to `lastGood` without a download, at the cost of up to three releases on disk per OS (roughly 1.2 GB Flatcar, 3 GB CoreOS, 2.5 GB Bluefin).

Booty migrates older data directories on start-up, idempotently and logged as `… release layout migrated`: Flatcar's top-level `flatcar_production_pxe.vmlinuz`/`flatcar_production_pxe_image.cpio.gz` (plain files from the earliest releases, or symlinks into `flatcar/<version>/`) end up in `flatcar/<version>/` with `current` pointing at the version from `version.txt`; CoreOS's flat `fedora-coreos-<version>-live-*` files move into `coreos/<version>/`; Bluefin's `bluefin/<version>/` with `current`/`previous` only gains `lastGood`. `flatcar_pin.txt` and `version.txt` stay where they are. The top-level names remain as symlinks into `<os>/current` (`data/flatcar_production_pxe.vmlinuz -> flatcar/current/flatcar_production_pxe.vmlinuz`), so `/data/<old name>` keeps serving the current release; `/data/<os>/<version>/…` serves any retained one.

### Per-host `targetVersion`

Each host may carry `targetVersion` (`/register`, `/hosts`, `/booty.json`): empty, or a cached release of the host's OS (`400` otherwise, naming the cached ones). Every render honours it: a Flatcar host's `/booty.ipxe` fetches `data/flatcar/<version>/…`, a CoreOS host's `data/coreos/<version>/…` with `VERSION` set to it, and a Bluefin host's `/bluefin/<mac>/` directory serves that release's netboot UKI, DDI, `SHA256SUMS`, BIOS `.linux`/`.initrd`/`.ucode` sections and a node Ignition whose sysexts, kubeadm sysext and install unit name it (the chainload and BIOS menus show its version). A `targetVersion` that has since been pruned falls back to the fleet target with a warning in the log. `/update-check` answers from the same effective target, so setting `targetVersion` on a host is how you hold it on, or move it to, a specific release: kured drains and reboots it, and it comes back running that release.

## Configuration view and editor

`GET /config` shows the running configuration and the Butane templates in play, so you do not have to guess which flag, environment variable or default a Booty instance ended up with:

```json
{"settings":[{"key":"serverIP","value":"192.168.1.10","source":"flag","redacted":false}, ...],
 "templates":{"default":{"name":"config/ignition.yaml","source":"file","writable":true},
              "hosts":[{"mac":"aa:bb:cc:dd:ee:01","hostname":"n1","name":"config/n1.yaml"}]}}
```

* `settings` lists every flag, sorted by key, with its effective value rendered as a string and its `source`: `flag` when it was passed on the command line, `env` when `BOOTY_<FLAG>` (or one of the legacy `IGNITION_FILE`/`HARDWARE_MAP`/`FLATCAR_VERSION_PIN` names) is set, otherwise `default`. Values Booty computes itself -- the autodetected `serverIP`, the derived `serverHttpPort`, the build stamp -- count as `default`. `joinString` and `githubToken` are always `redacted:true` and shown as `••••` when non-empty (empty stays empty); `joinStringFile` is a path, not the secret, and is shown as-is.
* `templates.default` is `--ignitionFile`; `source` is `embedded` while the file does not exist and Booty renders its built-in minimal template, `file` otherwise. `templates.hosts` lists registered hosts with their own `ignitionFile`.
* `writable` says whether saving the template from the UI would work. Booty probes the file (opened for writing and closed, never modified) or, for a missing file, its directory. When the probe fails the response carries `readOnlyReason`, e.g. `"/data/config/ignition.yaml is read-only (read-only file system)"`.

The template editor behind it:

| Endpoint | Does |
|---|---|
| `GET /config/template?name=config/ignition.yaml` | Returns `{name, source, writable, readOnlyReason?, content}`. `name` defaults to `--ignitionFile`; it must be a relative path inside `--dataDir` ending in `.yaml`, `.yml` or `.bu`, and may not name `hardware.json`, the pin file, temp files or the OCI blob store (400). A missing file is a 404, except for the default template, which returns the embedded Butane with `source:"embedded"`. |
| `POST /config/template/validate` | Body `{"name","content","mac"?}`. Renders the template exactly like a boot would and translates it with Butane; answers `{"ok","ignition","entries":[{"kind":"warning|error","message"}]}` with HTTP 200 even when the template is broken (`ok:false`). With `mac` the registered host's hostname/image are used (404 if unknown); without it a dummy host (`example`, `00:00:00:00:00:00`) stands in. `{{ .JoinString }}` is the static join string; with `--kubeadmJoin=auto` it is the placeholder `kubeadm join <auto>` -- validation never mints a token. |
| `PUT /config/template` | Body `{"name","content"}`. Validates with the dummy host first (`400 {"error":"template does not validate","entries":[...]}`), refuses read-only targets (`409 {"error":"template is read-only","reason":"..."}`), then writes the file atomically inside `--dataDir`, logs `Ignition template saved via API` with name and size, and answers like the GET. Never writes outside `--dataDir`. |

**GitOps and ConfigMap mounts.** If `/data/config/` is a Kubernetes ConfigMap (as in [examples/k8s.yaml](examples/k8s.yaml)) or otherwise mounted read-only, the view still works but shows `writable:false` with the reason, the UI disables saving, and a `PUT` is answered with 409 -- edit the source of truth (Git, the ConfigMap) instead. Validation is always available.

**Trust model.** The editor has no authentication, like the rest of Booty (see [Trust model](#trust-model)): anyone who can reach the HTTP port can read the effective configuration (secrets redacted) and, on a writable data directory, replace the Butane template every host boots with. Keep the port on the boot VLAN or mount the template read-only if that is not acceptable.

## Bluefin Server

[Bluefin Server](https://github.com/projectbluefin/server) is an image-based server OS composed from freedesktop-sdk: a read-only erofs `/usr` verified by dm-verity, pinned by a **signed UKI**, with Kubernetes, ZFS and friends as opt-in `systemd-sysext` images. It boots **diskless by default**: the firmware fetches the signed netboot UKI over UEFI HTTP Boot, the UKI's initrd downloads the OS disk image (DDI) from the same directory into RAM, verifies it and runs from a tmpfs root. Booty serves that directory per host, answers the firmware over ProxyDHCP, hands every host its own Ignition config and can install a host to disk.

**Firmware: UEFI HTTP Boot, UEFI PXE with Secure Boot off, or legacy BIOS PXE (diskless only).** The UKI's kernel command line is embedded and signed, and names the DDI it downloads relative to the URL it was booted from (`rd.systemd.pull=...,bootorigin:rootdisk:bluefin-server_<version>.raw`). UEFI HTTP Boot provides that origin and works with Secure Boot on, provided the firmware trusts the image's keys (the release's ESP image enrolls them; see [projectbluefin/server](https://github.com/projectbluefin/server)). UEFI firmware without HTTP Boot can [chainload the UKI from iPXE](#chainloading-from-ipxe-secure-boot-off) as long as Secure Boot is off: that needs UEFI PXE, so switch legacy/CSM boot off for it. Machines that only netboot in legacy BIOS mode (or that you keep in CSM mode) can still [boot Bluefin diskless from BIOS iPXE](#legacy-bios-diskless-boot), but never install it.

### Releases

Booty tracks the `v<version>` releases of `--bluefinRepo` (default `projectbluefin/server`, e.g. tag `v20260927.123` for version `20260927.123`, compared as dotted numbers) through the GitHub releases API (unauthenticated: 60 requests/hour, one request per `--updateSchedule` tick; `--githubToken`/`BOOTY_GITHUBTOKEN` is sent as a bearer token). The newest published, non-prerelease release carrying `bluefin-server-netboot_<version>.efi`, `bluefin-server_<version>.raw`, `SHA256SUMS` and `SHA256SUMS.gpg` wins; `installer-v*` and `bluefin-server-v*` tags are ignored. `--bluefinVersion=20260927.123` pins a release (fetched directly, without the API).

`--bluefinOCI=ghcr.io/projectbluefin/bluefin-server` reads the same files from an [ORAS](https://oras.land) artifact instead of GitHub: one layer per file named by its `org.opencontainers.image.title` annotation, tagged `<version>` and `latest`. Booty resolves `latest` (or the pinned version's tag) to its manifest and takes the version from the netboot UKI's file name. Pulls are anonymous; `--githubToken` is used for `ghcr.io` only. Prefix the reference with `http://` for a plain-HTTP registry (`http://<registry-host>:30500/bluefin-server`).

For the chosen release Booty downloads `SHA256SUMS` and `SHA256SUMS.gpg`, the netboot UKI and the DDI, plus the optional sysexts when `SHA256SUMS` lists them: `zfs_<version>.raw.zst`, `kubestellar_<version>.raw.zst`, `kubeadm_<version>.raw.zst` and the single `k0s-<k0s-version>.raw.zst`. Every file is verified against `SHA256SUMS`. With `--bluefinKeyring=<OpenPGP public keyring>` (binary as `gpgv --keyring` takes it, or ASCII-armored) Booty first checks the detached `SHA256SUMS.gpg` signature and **fails closed**: a missing or bad signature leaves the served release where it was. Without it Booty trusts `SHA256SUMS` from the release; the nodes check the signature themselves either way (`verify=signature` with the image's keyring). The sysexts are decompressed on download, because Ignition cannot decompress zstd, and `manifest.json` records the digest of the decompressed `.raw`.

Everything lands in `data/bluefin/<version>/` with a `manifest.json` (`version`, `netbootUKI`, `ddi`, `sysexts` `{name: {file, sha256}}`, `sha256sums`). `data/bluefin/current` is repointed atomically only after every file is on disk and verified; `data/bluefin/previous` keeps the release it replaced, so hosts that were booting it can finish, `data/bluefin/lastGood` the last release the fleet was healthy on, and every release directory none of those (or a host's `targetVersion`) names is pruned (see [Releases and retention](#releases-and-retention)). A manifest whose files went missing, or one left over from the retired `installer-v*` releases, resets the version at start-up so the next tick downloads afresh. `/info` shows `"bluefin":{"version","pinnedVersion"}`, `/version.json` gains `bluefin` and `/version.txt` a `BLUEFIN_VERSION=` line.

### How a Bluefin host boots

1. The firmware broadcasts a DHCPDISCOVER with option 60 `HTTPClient` and architecture `0x0010` (x86-64 HTTP). With `--proxyDHCP`, Booty answers a registered `os: bluefin` host (or any unknown MAC under `--autoRegister=bluefin`) with a proxy OFFER: option 60 `HTTPClient` and the boot file URL `http://<serverIP>:<port>/bluefin/<mac>/bluefin-server-netboot.efi` (MAC with dashes). This works with or without `--secureBoot`; all other HTTP Boot clients keep the [Secure Boot](#secure-boot-uefi-http-boot) behaviour. Without `--proxyDHCP`, give each Bluefin host that URL as its HTTP Boot file in your DHCP server.
2. `GET /bluefin/<mac>/<anything>.efi` returns the host's target release's netboot UKI -- its `targetVersion`, else `current` (`bluefin-server-netboot_<version>.efi` returns that release's for any retained release, and 404 for any other version) (`application/efi`, `HEAD` answered like `GET`). The fetch records the host's `booted`/`ip` for the fleet view.
3. The initrd fetches `bluefin-server_<version>.raw`, `SHA256SUMS` and `SHA256SUMS.gpg` from the same directory: `/bluefin/<mac>/` serves the DDI of any retained release (404 for any other version) and the target release's checksum files.
4. With no Ignition credential set, the initrd `HEAD`s `/bluefin/<mac>/bluefin-node.ign` and, if it exists, runs Ignition with it (below). 404 means nothing to configure.

Every route under `/bluefin/<mac>/` answers only registered Bluefin hosts (404 otherwise, the MAC may be written with colons or dashes); `?preview=1` shows a file without recording anything.

### Chainloading from iPXE (Secure Boot off)

Older UEFI firmware has PXE but no HTTP Boot (an HP EliteDesk 800 G1, for one). Such a machine UEFI-PXE-boots into Booty's `ipxe.efi` as described in [Booting](#booting-dhcp-and-the-ipxe-bootloaders) (or through [ProxyDHCP](#zero-touch-dhcp-proxydhcp)), and `/booty.ipxe` then chainloads the netboot UKI itself. Two things make that work only with **Secure Boot off**: iPXE hands the UKI a command line, which systemd-stub uses *instead of* the embedded one when Secure Boot is off (and ignores when it is on), and iPXE sets no boot origin, so `bootorigin` pulls would be skipped. Booty therefore passes the UKI's own command line with the pull made explicit:

```
chain http://<serverIP>:<port>/bluefin/<mac>/bluefin-server-netboot_<version>.efi usrhash=... root=tmpfs \
  rd.systemd.pull=raw,machine,verify=signature,blockdev:rootdisk:http://<serverIP>:<port>/bluefin/<mac>/bluefin-server_<version>.raw ... console=ttyS0,115200
```

Booty reads the current release's UKI `.cmdline` PE section once per release, replaces the single `rd.systemd.pull=<options>,bootorigin:rootdisk:<name>` argument with `rd.systemd.pull=<options>:rootdisk:http://<serverIP>:<port>/bluefin/<mac>/<name>` (every other option, `verify=signature` included, and every other argument unchanged; `<mac>` with dashes) and names the UKI by version, so the UKI iPXE fetches always matches the command line even when a new release lands in between. The initrd then pulls the DDI, `SHA256SUMS` and `SHA256SUMS.gpg` from that directory and finds `bluefin-node.ign` next to them, exactly as with HTTP Boot. The menu boots this after 5 s (*Boot from disk* / *iPXE shell* / *Reboot* stay available); a failed chain falls back to the disk after a 5 s prompt. The UKI fetch does the boot and install bookkeeping, the menu itself records nothing.

Booty falls back to the *switch to UEFI HTTP Boot* menu, which says why, when the host's last boot came through Secure Boot (the `secureBoot` host flag), no release is cached, or the UKI's command line is unusable: no `.cmdline` section, not exactly one `bootorigin` pull, a DDI other than the release's, or characters iPXE would expand (`$`, quotes, backslashes) -- the last three are logged as errors. A host in `mode: installed` without `doInstall` gets a script that just boots its disk, on every platform.

With Secure Boot off nothing verifies the UKI itself; `/usr` is still checked against the `usrhash=` Booty passes (dm-verity), and the DDI against the release's signed `SHA256SUMS`.

### Legacy BIOS diskless boot

The same `/booty.ipxe` script branches on iPXE's `${platform}`: a BIOS machine that PXE-boots Booty's `undionly.kpxe` (DHCP architecture `00:00`, as in [Booting](#booting-dhcp-and-the-ipxe-bootloaders), or through [ProxyDHCP](#zero-touch-dhcp-proxydhcp)) lands in a `:bios` branch that boots Bluefin **diskless** by default after 5 s (*Boot from disk* / *iPXE shell* / *Reboot* stay available). There is no systemd-stub on BIOS, so iPXE boots the UKI's parts as a plain bzImage:

```
cpuid --ext 29 || goto bios-not64
imgfree
kernel http://<serverIP>:<port>/bluefin/<mac>/bluefin-server-netboot_<version>.linux <the same command line as the EFI chain>
initrd http://<serverIP>:<port>/bluefin/<mac>/bluefin-server-netboot_<version>.ucode    (only when the UKI has a .ucode section)
initrd http://<serverIP>:<port>/bluefin/<mac>/bluefin-server-netboot_<version>.initrd
boot
```

`/bluefin/<mac>/bluefin-server-netboot_<version>.{linux,initrd,ucode}` serve that PE section of the current or previous release's netboot UKI straight out of the file (HEAD and Range work; 404 for a section the UKI lacks, another version or a non-Bluefin MAC). BIOS iPXE concatenates every `initrd` in fetch order (microcode first, as systemd-stub does), so no `initrd=` argument is passed; iPXE runs as i386 on BIOS even on 64-bit CPUs, hence the `cpuid --ext 29` long-mode check (the bundled `undionly.kpxe` includes `cpuid`), which drops a 32-bit machine to the shell with a message. The command line is derived exactly as for the EFI chain, with the same fail-closed rules, and Secure Boot plays no part (the `secureBoot` host flag does not block this branch). The `.linux` fetch records the boot (`netbootPlatform: pcbios`) like the UKI fetch does; the initrds record nothing. From there the boot is the same as on UEFI: the initrd pulls the DDI, `SHA256SUMS` and `SHA256SUMS.gpg` from `/bluefin/<mac>/` and runs Ignition with `bluefin-node.ign` from next to them.

**Diskless only.** Installing, systemd-sysupdate updates and boot-counted rollback need UEFI and systemd-boot. A host with `doInstall` that boots in BIOS mode gets a menu saying the install needs UEFI and boots diskless without installing: its node config carries no `booty-install.service` after a BIOS netboot, nothing is stamped that could count as a finished install, and `doInstall` stays set for its next UEFI boot. An installed host being reinstalled defaults to *Boot from disk* on BIOS.

**Trust.** Nothing is signature-checked by the firmware on this path, the same as the Secure-Boot-off chain: the kernel, the initrd and the `usrhash=` in the command line are trusted as they arrive from Booty over plain HTTP. The initrd then checks the DDI against the release's `SHA256SUMS` and its signature (`verify=signature` with the image's keyring), and dm-verity checks every `/usr` block against that `usrhash=`. Someone able to tamper with the boot network can therefore substitute a kernel, initrd or hash; keep the boot VLAN trusted, or use UEFI HTTP Boot with Secure Boot where the hardware allows.

**Testing it.** For a SeaBIOS QEMU run the NBP is **`boot/undionly.kpxe`**. Its embedded script ([`boot/embed.ipxe`](boot/embed.ipxe)) runs `dhcp`, then `chain tftp://${next-server}/booty.ipxe` (Booty's TFTP stub, which chains `http://<serverIP>:<port>/booty.ipxe?mac=${mac}`), falling back to `chain http://${next-server}/booty.ipxe?mac=${mac}` (port 80). With QEMU's slirp TFTP (`-netdev user,tftp=<dir>,bootfile=undionly.kpxe`), put `undionly.kpxe` and a `booty.ipxe` holding `#!ipxe` / `chain http://<booty>:<port>/booty.ipxe?mac=${mac}` in `<dir>` (slirp's `next-server` is 10.0.2.2); the real script then comes from Booty.

### The node's Ignition config

`bluefin-node.ign` is an Ignition **spec 3.6.0** config rendered for the host. A diskless node runs Ignition on **every boot** (its root is a fresh tmpfs), so everything in it is idempotent: files are written with `overwrite: true` and no disk is ever wiped unless the host asks for an install.

* `hostname` and `sshkeys` builtins (`--builtin`): `/etc/hostname`, and `root`'s `sshAuthorizedKeys` from `--sshAuthorizedKeys`/`--sshAuthorizedKeysFile`.
* `booted`, `update` and `health` builtins, the same units a Flatcar host gets (see [Composition](#composition)) with the scripts at `/etc/booty/update-check` and `/etc/booty/health-report` (next to `kubeadm-join.sh`; `/etc` is writable on a diskless node): `booty-booted.service` POSTs `/booted`, `booty-update.timer` runs the update check every 10 minutes and touches `/var/run/reboot-required` for kured when the node is behind its target release, and `booty-health.service` POSTs `/health` after `multi-user.target`. Each is dropped with its `--builtin` toggle.
* `stateDisk` (host field, e.g. `/dev/sdb`): keeps `/var` on that disk, as [`var-on-disk.ign`](https://github.com/projectbluefin/server/blob/main/tests/fixtures/ignition/var-on-disk.ign) upstream: GPT partition 1 and an xfs filesystem both labelled `bluefin-var`, created when missing and never wiped (`wipeTable`/`wipePartitionEntry`/`wipeFilesystem` false), mounted by an enabled `var.mount` (`WantedBy=local-fs.target`). The filesystem also sets `path: /var` so the config's own `/var` files (the k0s sysext and state) are written onto the disk rather than into the tmpfs `var.mount` later covers. Without a state disk `/var` is RAM and starts empty on every boot.
* `extensions` (host field, any of `zfs`, `kubestellar`, `k0s`; `kubestellar` requires `k0s`): `zfs` and `kubestellar` are written to `/etc/extensions/<name>_<version>.raw` (the versioned name matters: the image's `extension-release.<name>_<version>` must match it), `k0s` to `/var/lib/k0s/k0s.raw` plus an enabled `k0s-first-boot.service`, which merges it and starts k0s. Each is fetched from `http://<serverIP>/data/bluefin/<version>/...` with `verification.hash: sha256-<digest of the decompressed file>`.
* Under `--clusterDistribution=k0s` every Bluefin host is a cluster node and gets the `k0s` sysext implicitly, plus its [k0s pieces](#managed-k0s): a worker `/etc/k0s/token` (mode 0600; `k0s-first-boot` starts `k0sworker.service` when that file exists), a controller `/etc/sysconfig/k0s` with `K0S_CONTROLLER_ARGS=-c /etc/k0s/k0s.yaml --enable-worker --disable-components=helm,autopilot`, `/etc/k0s/k0s.yaml`, the PKI and token-Secret manifest and the `booty-cluster-ready`/`booty-cni-apply` units (`k0scontroller.service` otherwise). A diskless controller needs a `stateDisk` (or an install) to keep etcd; `/cluster` warns about one without.
* Under `--profile=kubeadm-worker` with an external kubeadm cluster, every Bluefin worker gets the `kubeadm` sysext and its join ([Bluefin hosts joining a kubeadm cluster](#bluefin-hosts-joining-a-kubeadm-cluster)).
* `doInstall` with `installDisk` (below): `booty-install.service`.
* The host's own `ignitionFile` (a Butane or Ignition template, rendered like for Flatcar) is merged last, so it wins. The global `--ignitionFile` template is **not** merged into Bluefin hosts.

### Bluefin hosts joining a kubeadm cluster

With `--profile=kubeadm-worker` and an external kubeadm cluster (`--controlPlane=external`, the default), every Bluefin host joins it as a worker the way a Flatcar host does -- unless it is `role: control-plane`, runs the `k0s` extension or installs to disk on this boot. It gets the same join string (`--kubeadmJoin=static`, or under `auto` a one-hour token minted on the initrd's `HEAD` of `bluefin-node.ign` and reused by the `GET` right after; previews never mint), the same `KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false` in `/etc/default/kubelet`, and a fresh `kubeadm reset -f` + `kubeadm join` on every diskless boot.

* **Needs the `kubeadm` sysext in the release** (`kubeadm_<version>.raw.zst`, locked to the image version, from projectbluefin/server 2026.09.1). It provides `kubelet`, `kubeadm`, `kubectl`, `crictl`, `containerd`, `ctr` and `runc` in `/usr/bin`, the CNI plugins in `/usr/libexec/cni`, and `containerd.service`/`kubelet.service` with kubeadm's `10-kubeadm.conf`; it enables nothing and puts nothing in `/opt`, which stays writable for Cilium's CNI install. A release without it fails closed: Booty logs `Bluefin release has no kubeadm sysext; serving the node config without the kubeadm join` at error level and the host boots diskless without joining.
* **What the node config adds:** `/etc/extensions/kubeadm_<version>.raw`, `/etc/default/kubelet`, enabled `containerd.service` and `kubelet.service`, and an enabled `booty-kubeadm-join.service` (`Wants=`/`After=network-online.target containerd.service`, `After=systemd-sysext.service`) running `/etc/booty/kubeadm-join.sh`: Flatcar's `join.sh` with `PATH=/usr/bin:/usr/sbin` instead of `/opt/bin`. The sysext's units only appear once `systemd-sysext` merges it, after the boot's presets were applied, so the script runs `systemctl enable --now containerd.service kubelet.service` itself before `kubeadm reset -f` and `<join string> --cri-socket unix:///run/containerd/containerd.sock --node-name <hostname>`.
* **Rejoining under an existing Node.** kubeadm refuses to join while a Node of the same name is `Ready` (`a Node with name ... already exists in the cluster`). Flatcar relies on its reboot outlasting the node controller's grace period, and on `Restart=on-failure RestartSec=30s` when it does not. The Bluefin unit retries with backoff (`RestartSec=15s` growing to 2 min), logs every attempt and says when the old Node is still `Ready`, and gives up after 30 attempts -- about the one-hour token lifetime -- with exit status 3 (`RestartPreventExitStatus=3`); reboot to retry with a fresh token.
* **containerd disk.** `--containerdDisk` (e.g. `/dev/sda`) is expected to carry the ext4 filesystem a Flatcar worker formats on it, and a Bluefin worker never formats, wipes or relabels it. Ignition does not touch it either: there is no `storage.filesystems` entry, because Ignition 3.6 with `wipeFilesystem: false` fails the boot on a type or label mismatch and formats a blank disk. Instead `var-lib-containerd\x2ddisk.mount` mounts it at `/var/lib/containerd-disk` (`nofail`, pulled in only by containerd), `booty-containerd-disk-dir.service` creates `bluefin/` on it (it asserts the mount point and refuses a symlink), and `var-lib-containerd.mount` bind-mounts `/var/lib/containerd-disk/bluefin` onto `/var/lib/containerd`, required by a `containerd.service` drop-in. A missing disk fails containerd and the join, not the boot. Everything outside `bluefin/` stays as the Flatcar node left it:

  ```
  /dev/sda (ext4, label ssd)
  |-- io.containerd.*, tmpmounts, ...   the Flatcar node's containerd root, untouched
  `-- bluefin/                          the Bluefin node's containerd root
  ```

* **iSCSI.** From projectbluefin/server 2026.09.2 the base image ships open-iscsi with `iscsid.socket` enabled, so iSCSI CSI drivers attach volumes on Bluefin workers without anything in the node config. Earlier releases have no iSCSI userspace; keep iSCSI workloads off Bluefin workers running them.

### Installing to disk

Set `doInstall` and `installDisk` (a `/dev/...` path, required for Bluefin; it must differ from `stateDisk`) and boot the host. Its next diskless boot gets an enabled oneshot `booty-install.service` (`After=network-online.target run-bluefin-boot.mount`, `Requires=run-bluefin-boot.mount`, `WantedBy=multi-user.target`) running

```
/usr/bin/systemd-sysinstall --erase=yes --confirm=no --variables=yes --reboot=yes \
  --definitions=/run/bluefin/boot/bluefin/repart.d \
  --kernel=/run/bluefin/boot/EFI/Linux/bluefin-server-<version>.efi <installDisk>
```

which copies the running DDI's `/usr` and disk UKI to the disk, registers the boot entry and reboots. Once the install is counted as done the host's `mode` becomes `installed` and `doInstall` is cleared: ProxyDHCP then offers it **no** HTTP Boot file (the firmware falls through to the disk) and its `.efi` URL answers 404. Set `doInstall` again to reinstall; set `mode` back to `diskless` (or leave it empty) to netboot it again. When the install counts as done follows `--doInstallClearOn`:

* `ignition` (default): the `bluefin-node.ign` `GET` that carries the install unit.
* `next-boot`: Booty stamps `installServedAt` on every UKI fetch while `doInstall` is set; the first UKI fetch at least `--installMinDuration` (default `3m`) later counts as "rebooted after installing" and is answered 404 (so that very boot goes to the disk). An earlier fetch means the install failed, and it runs again.
* `booted`: behaves like `next-boot` for Bluefin (the installed disk runs no Booty Ignition, so it never POSTs `/booted`; waiting for it would reinstall on every boot). `POST /booted` still marks an installing Bluefin host installed.

Installed nodes are managed by the image from then on: they update themselves with systemd-sysupdate and do not fetch Booty's Ignition config again.

### Updates

A diskless host re-images from Booty on every boot, so it runs whatever `/bluefin/<mac>/` serves: its `targetVersion`, else the `current` release. Its `booty-update.timer` asks `GET /update-check` every 10 minutes, and once the node's `VERSION_ID` differs from that target the answer is `rebootRequired:true` with the reason `bluefin: re-image on reboot into <target>` and the node touches `/var/run/reboot-required`, so kured drains and reboots it into the new release exactly like a Flatcar host (see [Fleet status](#fleet-status)). Rolling back is setting `targetVersion` to a retained release. An installed host updates itself with systemd-sysupdate and runs no Booty units.

## Zero-touch DHCP (ProxyDHCP)

**EXPERIMENTAL -- not yet exercised in production.**

Normally step 1 above needs the network's DHCP server to hand out `next-server` and `filename`. With `--proxyDHCP` Booty instead acts as a *ProxyDHCP* server (PXE 2.1 spec section 2.2, the model used by pixiecore and netboot.xyz): the existing DHCP server keeps assigning addresses untouched, and Booty only adds the boot information.

1. The PXE firmware broadcasts a DHCPDISCOVER with option 60 `PXEClient` and option 93 (client architecture, RFC 4578).
2. Booty answers on UDP 67 with a DHCPOFFER that offers **no address** (`yiaddr` 0.0.0.0) but carries `siaddr` = `--serverIP` and PXE vendor options (option 43: discovery control, a boot-server list containing only Booty, a one-entry "Booty" menu with a zero-timeout prompt). The real DHCP server's OFFER supplies the address.
3. After it has its address the client sends a DHCPREQUEST to Booty on UDP **4011**; Booty replies with a DHCPACK naming the boot file (in both the `file` field and option 67) and option 66 = `--serverIP`.
4. The client fetches that file over TFTP from Booty and the normal flow continues.

Booty only answers packets that carry `PXEClient` in option 60 *and* an architecture in option 93; everything else (ordinary DHCP clients, Booty's own replies, requests whose server identifier names another server) is ignored. Relayed requests (`giaddr` set) are ignored unless `--proxyDHCPRelay` is given -- relayed PXE is rare and easy to get wrong, so it is opt-in. A DHCP server that *also* sets `next-server`/`filename` can coexist with ProxyDHCP: both answer and the client uses whichever boot information it picks, which is fine as long as both point at Booty.

| Option 93 architecture | Boot file sent |
|---|---|
| 0 x86 BIOS | `undionly.kpxe` |
| 6 x86 UEFI (32-bit) | `ipxe.efi` (Booty only ships x86-64; logged as a warning) |
| 7, 9 x86-64 UEFI | `ipxe.efi` |
| 11 ARM64 UEFI | `ipxe-arm64.efi` (not shipped -- logged as a warning, the boot will fail) |
| anything else | `undionly.kpxe` (logged as a warning) |
| user class `iPXE` (any arch) | `booty.ipxe` |
| option 60 `HTTPClient` (UEFI HTTP Boot), registered Bluefin host | its netboot UKI URL, or nothing once installed; see [Bluefin Server](#bluefin-server) |
| option 60 `HTTPClient` (UEFI HTTP Boot), any other host | ignored unless `--secureBoot` is on, see [Secure Boot](#secure-boot-uefi-http-boot) |

`--efiBootloader=snponly` swaps `ipxe.efi` for `snponly.efi` in the x86-64 UEFI rows (and the matching shim for HTTP Boot clients); the default `ipxe` keeps today's files.

The last row is iPXE's own second-stage DHCP: once `undionly.kpxe`/`ipxe.efi` is running it repeats DHCP with user class `iPXE`. Handing it another iPXE binary would loop, so Booty gives it the `booty.ipxe` stub instead. iPXE resolves a bare filename against `tftp://${next-server}/`, and `${next-server}` falls back to the `siaddr` of the ProxyDHCP reply when the real DHCP server's `siaddr` is empty (iPXE keeps ProxyDHCP settings in a lower-priority `proxydhcp` settings block that is consulted whenever the primary DHCP settings lack a value), so this resolves to Booty. If your iPXE build has an embedded script the filename is ignored and the embedded script runs instead.

Ports 67 and 4011 are privileged, so the container needs `--network=host`/`hostNetwork: true` and `CAP_NET_BIND_SERVICE` (already granted in the examples above). `--proxyDHCPListen` takes an IPv4 address or an interface name; prefer the interface name on multi-homed hosts, because a socket bound to a unicast address does not receive the broadcast DHCPDISCOVERs on Linux. Only iPXE's `ipxe.efi` needs to be present in `--dataDir` for UEFI clients; `undionly.kpxe` and `booty.ipxe` are built in. For tests without root the two ports can be overridden with the environment variable `BOOTY_PROXYDHCPPORTS=1067,5011` (test-only, not a flag).

## Secure Boot (UEFI HTTP Boot)

Machines with Secure Boot enabled refuse Booty's own `ipxe.efi`: it is not signed by anything their firmware trusts. `--secureBoot` makes them bootable without disabling Secure Boot and without Booty holding any signing key, by handing them a **Microsoft-signed shim that loads a signed iPXE**, over UEFI HTTP Boot. It is off by default and changes nothing for existing deployments while off (HTTP Boot DISCOVERs are ignored, logged once, except from Bluefin hosts, which are always offered their own signed UKI; nothing is downloaded).

**It requires `--proxyDHCP`.** The signed iPXE build has no embedded Booty script, so it can only find Booty through the DHCP answer for iPXE's own user class, which is what the ProxyDHCP server provides. Nothing else in the network's DHCP server needs to change.

### How it works

1. The firmware's *UEFI HTTPv4* boot entry broadcasts a DHCPDISCOVER with option 60 `HTTPClient:Arch:00016:UNDI:003001` and option 93 = `0x0010` (x86-64; `0x000F` is 32-bit x86, `0x0013` ARM64).
2. Booty's ProxyDHCP server answers with a *proxy* OFFER: `yiaddr` 0.0.0.0, option 60 `HTTPClient`, option 67 = `http://<serverIP>[:<serverHttpPort>]/boot/sb/ipxe-shimx64.efi` (`snponly-shimx64.efi` with `--efiBootloader=snponly`), no option 43. That is the shape the UEFI 2.10 HTTP Boot client accepts from a proxy: an IP-literal `http://` URI next to the real DHCP server's address offer. It never sends a REQUEST to the proxy and does not use port 4011, so Booty ignores REQUESTs from HTTP Boot clients. The whole OFFER stays under the client's 1472-byte limit.
3. The firmware fetches the URL (HEAD, then GET; it follows no redirects and insists on `Content-Type: application/efi` or a `.efi` suffix, `Content-Length` and no chunked encoding -- the `/boot/` handler is written for exactly that, including `//` in the path, which the shim produces).
4. `ipxe-shimx64.efi` (dual-signed with the Microsoft UEFI CA 2011 *and* 2023, from [ipxe/shim](https://github.com/ipxe/shim/releases/tag/ipxe-16.1)) verifies and loads `ipxe.efi` **from the same URL directory** (it derives the name from its own: `ipxe-shimx64.efi -> ipxe.efi`, `snponly-shimx64.efi -> snponly.efi`), which is why the signed set lives under `/boot/sb/` and the unsigned embedded `ipxe.efi` stays at `/boot/ipxe.efi` for TFTP/PXE clients.
5. The signed iPXE (from [ipxe v2.0.0](https://github.com/ipxe/ipxe/releases/tag/v2.0.0) `ipxeboot.tar.gz`, `x86_64-sb/`, signed by the iPXE CA the shim trusts) runs its default `autoboot`: DHCP with user class `iPXE`, which the ProxyDHCP server answers with `booty.ipxe` as for any other iPXE, and Booty's normal menu flow takes over. Every later `kernel`/`chain` goes through the firmware's `LoadImage`, so kernels must be trusted by the machine's `db` -- the table below.

### What boots under Secure Boot

| OS | Kernel signer | Boots with stock (Microsoft) keys? | What Booty does for a Secure Boot host |
|---|---|---|---|
| Fedora CoreOS | Fedora CA (via Fedora's Microsoft-signed shim) | **yes** | `shim http://<booty>/boot/secureboot/fedora/shimx64.efi` before the `kernel` line, so the live kernel is verified against the Fedora vendor certificate instead of `db` (verified in QEMU: FCOS boots, `SecureBoot enabled` inside the guest) |
| Flatcar | *Flatcar Container Linux Secure Boot Development CA* (self-signed, not Microsoft-trusted; Flatcar's own shim is signed by it too) | no | with `--secureBootTrusted=flatcar`: the normal `kernel` line, which the firmware accepts once the CA is enrolled (verified in QEMU). Without it: the **refusal menu** below |
| Bluefin Server | the image's own Secure Boot keys (signed netboot/disk UKIs and systemd-boot) | only once its keys are enrolled | never through iPXE: ProxyDHCP hands the firmware the signed netboot UKI directly (see [Bluefin Server](#bluefin-server)); a Bluefin host that reaches iPXE anyway gets the *switch to UEFI HTTP Boot* menu |
| unregistered MAC | -- | -- | the usual unknown-host menu; nothing is recorded |

`--secureBootTrusted` (comma separated; `microsoft` is always implied, `flatcar` is the only other value) is **your assertion** about the fleet's firmware `db`, nothing Booty can check: with `flatcar` in the list a Secure Boot Flatcar host gets the plain kernel and fails at iPXE (`Error 0x7f04819a`, image verification) if the CA is in fact missing on that machine; without it Booty refuses rather than letting the boot fail. It is reported in `/info` and the UI. Flatcar has already rotated this "development" CA once; when the extracted certificate changes between releases Booty logs a warning with both fingerprints, and machines have to enroll the new one.

### The `secureBoot` host flag

Each registered host carries `secureBoot: true|false` (in `/hosts`, `/booty.json`, `/cluster`'s host list and as a shield badge next to the OS in the UI). It records **which path the host's last boot script came through**: the signed iPXE from the HTTP Boot path fetches `/booty.ipxe?mac=…&sb=1` (its `autoexec.ipxe` adds the flag; the unsigned TFTP/PXE iPXE never does), so `sb=1` can only come from a firmware that accepted the Microsoft-signed shim, i.e. one that has Secure Boot enforcing. A plain `/booty.ipxe` fetch (the machine PXE-booted the unsigned `ipxe.efi`, so Secure Boot is off or the firmware fell back) clears the flag. It is not a firmware attestation and not user-editable -- `POST /register` stores whatever the UI sends back, the next boot overwrites it -- and `preview=1` fetches never touch it. The hardware map is rewritten only when the value changes.

### The refusal menu

A Secure Boot Flatcar host without `--secureBootTrusted=flatcar` gets this instead of a kernel (30 s timeout, *Boot from disk* is the default so an already-installed machine keeps working):

```
Booty: this machine reached Booty through Secure Boot, and Flatcar cannot boot that way.

The kernel is signed by the Flatcar Container Linux Secure Boot CA, which this
firmware does not trust (Booty was not started with --secureBootTrusted=flatcar).
  CA SHA256: ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2
  Download:  http://<booty>/boot/secureboot/flatcar-ca.der
Enroll that certificate in the firmware db (firmware setup UI, or sbctl enroll-keys
--microsoft with it added as a custom db key), restart Booty with
--secureBootTrusted=flatcar, or disable Secure Boot on this machine.

  Booty - Secure Boot: Flatcar refused - <hostname>
  [d] Boot from disk   [r] Reboot   [s] iPXE shell
```

The host's `doInstall` is left untouched and Booty logs `Secure Boot host cannot boot its OS; serving the refusal menu`. Every such host is also listed in `GET /info`'s `secureBoot.warnings` and in `GET /cluster`'s `warnings` (shown on the Overview page):

```
host aa:bb:cc:dd:ee:01 (flatcar): Secure Boot host; the Flatcar CA is not in --secureBootTrusted, boot refused
host aa:bb:cc:dd:ee:02 (bluefin): reached Booty through the signed iPXE; Bluefin boots its signed netboot UKI only through UEFI HTTP Boot (http://<booty>/bluefin/aa-bb-cc-dd-ee-02/bluefin-server-netboot.efi)
```

The Bluefin line appears for a Bluefin host whose last `/booty.ipxe` fetch came through the signed iPXE: it was sent the shim instead of its UKI, typically because another DHCP server answered its HTTP Boot request.

The warnings follow the host flag, so they stay until the machine boots again without `sb=1`, and they are computed even after `--secureBoot` is switched off.

### Enrolling the Flatcar CA

Booty extracts the CA from the Flatcar release it serves and offers it at `http://<booty>/boot/secureboot/flatcar-ca.der` (and `.pem`); the SHA256 is in `/info` (`secureBoot.flatcarCA.sha256`), on the Overview page (with copy and download buttons) and in the refusal menu, so you can compare it with what the firmware shows after enrolment. Enrol it in **`db`** (not MOK: iPXE loads the kernel through the firmware's `LoadImage`, which consults `db`/`dbx` only), keeping the Microsoft certificates so the iPXE shim keeps loading:

* **Firmware setup UI**: most vendors have *Secure Boot → Custom/Key Management → Enroll DB key from file*; put `flatcar-ca.der` on a FAT USB stick. Enrolling on the machine itself (not through Booty) is the point: the firmware, not the network, decides what it trusts.
* **`sbctl`** from a Linux system on that machine in setup mode: `sbctl create-keys`, convert the certificate to an EFI signature list (`cert-to-efi-sig-list -g "$(uuidgen)" flatcar-ca.pem flatcar-ca.esl`, from efitools), drop it into `sbctl`'s custom `db` directory (`/var/lib/sbctl/keys/custom/db/` in current versions; `sbctl enroll-keys --help` names it) and run `sbctl enroll-keys --microsoft --custom`. `--microsoft` keeps the Microsoft UEFI CA in `db`, which the iPXE shim and the Fedora shim need.
* **OVMF / QEMU** (what the S1/S2 lab did): `virt-fw-vars --input OVMF_VARS.secboot.fd --output vars-flatcar.fd --add-db <owner-guid> flatcar-ca.der` (from `python3-virt-firmware`), then boot the guest with `-drive if=pflash,unit=1,file=vars-flatcar.fd` next to `OVMF_CODE.secboot.fd`. `EnrollDefaultKeys.efi` alone only installs the Microsoft set.

After that, start Booty with `--secureBootTrusted=flatcar`; the Overview page's Secure Boot card lists `flatcar` under *Trusted CAs* and the Flatcar warnings disappear. When Flatcar rotates the CA (the `flatcarCA.flatcarVersion` and fingerprint change, logged at sync time), repeat the enrolment before the next reboot.

### Bluefin Server and Secure Boot

Bluefin Server signs its netboot UKI, disk UKI and systemd-boot with its own keys and boots straight from the firmware over UEFI HTTP Boot, so it needs neither the shim nor iPXE nor `--secureBoot`; ProxyDHCP answers Bluefin hosts with their UKI URL either way. The firmware must trust the image's keys: the release's `bluefin-server-netboot_<version>.esp.raw` carries enrollment payloads for machines in setup mode, see [projectbluefin/server](https://github.com/projectbluefin/server). The `secureBoot` host flag only ever reflects the iPXE path and says nothing about a Bluefin host's firmware.

**Microsoft UEFI CA 2011 expiry.** The 2011 CA expired on 2026-06-27; new hardware may only carry the 2023 CA, older hardware may only carry 2011. Both shims Booty serves are dual-signed, so either works. If you pin other versions, prefer dual-signed builds.

### Artefacts

`--secureBoot` syncs everything at startup and on every `--updateSchedule` tick into `--dataDir/secureboot/<bundle>/` behind a `secureboot/current` symlink, with a `manifest.json` (member, sha256, source URL) and older bundles pruned. The bundle name is the concatenation of the four version flags, so changing any of them produces a new bundle. Nothing is signed by Booty; every file is verified against a digest pinned in code for the default versions (checked 2026-09-26) and served read-only:

| Served at | File | From | Verification |
|---|---|---|---|
| `/boot/sb/ipxe-shimx64.efi`, `/boot/sb/snponly-shimx64.efi` (same bytes) | Microsoft-signed iPXE shim 16.1 | [ipxe/shim `ipxe-16.1`](https://github.com/ipxe/shim/releases/download/ipxe-16.1/ipxe-shimx64.efi) | sha256 `5eecca2780bd49c900565e124516a1bd666ec5e012825f34991b6ba1ef2fa6cf` (release asset digest); other `--secureBootIPXEShimVersion`s use the digest the GitHub releases API reports |
| `/boot/sb/ipxe.efi`, `/boot/sb/snponly.efi` | iPXE-CA-signed iPXE, `x86_64-sb/` members of `ipxeboot.tar.gz` | [ipxe/ipxe `v2.0.0`](https://github.com/ipxe/ipxe/releases/download/v2.0.0/ipxeboot.tar.gz) | tarball sha256 `01a526d4cc791fc30362259c609d6c506cc64a7bdff51b9a5eb788354e17eee1`; members `6558e378…8d33` / `b1e67c3e…e82a` recorded in the manifest and checked after extraction |
| `/boot/secureboot/fedora/shimx64.efi` | Fedora shim (dual-signed, Fedora vendor CA) | `shim-x64-16.1-7.x86_64.rpm` from `dl.fedoraproject.org` (Fedora 45; `updates/`, `releases/` and `development/` paths are tried) | rpm sha256 `04b7132d6316bff71427120b6aba85eb4490b2621ccb2f2559bd321ccb25f028`; other `--fedoraShimVersion`s download unverified with a warning |
| `/boot/secureboot/fedora/grubx64.efi` | Fedora GRUB | `grub2-efi-x64-2.12-64.fc44.x86_64.rpm` (Fedora 44 updates) | rpm sha256 `3ed403514d8a8973814d553acd230a25eb355e93ebbb5a9a7da91f8393f58802` |
| `/boot/secureboot/flatcar-ca.der`, `.pem` | Flatcar Secure Boot CA | `.vendor_cert` section of `flatcar_production_image.shim` of the Flatcar release Booty serves (SHA512 from its `.DIGESTS`) | fingerprint in `/info` and `secureboot/flatcar-ca.json`; 4757.2.0: `ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2` |

The RPMs are read in Go (lead, headers, `PAYLOADCOMPRESSOR`, cpio) -- Fedora 44/45 packages are zstd-compressed; an xz payload is refused with a clear error rather than pulling in another dependency. Everything under `/boot/sb/` and `/boot/secureboot/` is `data/secureboot/current/` (plus the two CA files); paths are cleaned without redirects, there is no listing and no way out of that directory.

`GET /info` gains `"secureBoot":{"enabled","ready","bundleVersion","trusted":[...],"bootURL","flatcarCA":{"flatcarVersion","sha256","subject","notAfter","url"},"warnings":[...]}`; `ready` means the current bundle is complete on disk, `warnings` lists the Secure Boot hosts that cannot boot their OS (see [The refusal menu](#the-refusal-menu)). `/config` lists the flags. The Overview page shows the block as a *Secure Boot* card (a one-line "Secure Boot: off" when the flag is not set).

### Testing without root

```
BOOTY_PROXYDHCPPORTS=1067,5011 booty --proxyDHCP --proxyDHCPListen 127.0.0.1 --secureBoot --serverIP 127.0.0.1 ...
```

then send a captured HTTP Boot DISCOVER (`pkg/dhcp/testdata/ovmf-httpv4-discover.hex` is a real OVMF one) to UDP 1067 and decode the OFFER; `curl -I http://127.0.0.1:8080/boot/sb/ipxe-shimx64.efi` must answer `200`, `Content-Type: application/efi` and a `Content-Length`, and so must `/boot/sb//ipxe.efi`.

Out of scope for now: HTTPS Boot (some vendor firmware is built to allow only `https://`), IPv6 HTTP Boot, ARM64 (warned, the shim is not shipped), MOK enrolment, and the distro shim-to-GRUB path.

## Trust model

Booty is meant to run on a network you control. It has **no authentication**: anyone who can reach the HTTP port can register hosts (and set their `targetVersion`), POST a health report for any registered MAC, read any registered host's rendered Ignition (including the kubeadm join string and the builtin SSH keys) via `/ignition.json?mac=` or `/ignition/builtin.json?mac=`, download boot artifacts, and -- through the [configuration editor](#configuration-view-and-editor) -- read the effective configuration (`joinString` and `githubToken` redacted) and, unless the data directory is mounted read-only, rewrite the Butane template every host boots with. This is inherent to PXE -- the booting machine has no credentials yet -- so treat the boot VLAN like you treat your DHCP server.

With `--kubeadmJoin=static` the join string is whatever you configured, typically a never-expiring token that grants node-join to anyone who reads it. Prefer `--kubeadmJoin=auto`: each real boot gets its own token that expires after `--joinTokenTTL` (1 h by default) and is deleted afterwards, so what leaks over the boot VLAN is worth at most one hour of node-join; previews never mint. k0s workers get the same one-hour tokens whenever Booty can reach the API (managed, or external with `--kubeconfig`), and the pre-shared 7-day token only while it cannot. Booty's own credential for that is its service account, scoped by the Roles in [examples/k8s.yaml](examples/k8s.yaml) to creating/listing/deleting Secrets in `kube-system` and reading `cluster-info`. The autopilot's `ClusterRole` adds node patching, pod eviction and, in Booty's namespace, creating privileged `hostPID` reboot Pods -- the power to drain and reboot any node, which is what a reboot controller is; it is only granted (and only used) once `--autopilot` is on, and P2 never exercises the write half (see [Cluster awareness and actuators](#cluster-awareness-and-actuators)).

What Booty does enforce: TFTP and HTTP file serving are confined to `--dataDir` (no path traversal, no directory listings), `hardware.json`, the version pin file, temp files, the OCI blob store and everything under `cluster/` (the cluster CA and bootstrap tokens, see [Cluster bootstrap](#cluster-bootstrap)) are never served over `/data/`, Ignition template paths from the hardware database must stay inside `--dataDir`, and all inputs (MACs, hostnames, OS names, roles, versions, `installDisk`, `stateDisk`, `extensions`, `mode`) are validated. A Bluefin host's `bluefin-node.ign` is as readable on the boot VLAN as any other host's Ignition (a k0s controller's carries the cluster CA key, exactly like a Flatcar controller's). Bluefin releases are verified against `SHA256SUMS`, and with `--bluefinKeyring` against its signature, before Booty serves them; the nodes verify the signature themselves as well. `--autoRegister` widens this further; read [Auto-registration](#auto-registration) before turning it on.

## Running as root / capabilities

Binding UDP 69 needs `CAP_NET_BIND_SERVICE` (as do UDP 67 and 4011 for `--proxyDHCP`); the ARP fallback used when a client does not supply its MAC needs `CAP_NET_RAW`. The container image runs as root for that reason -- drop everything else:

```
docker run --rm --network=host \
  --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  -v $PWD/data:/data ghcr.io/jeefy/booty:main --serverIP=192.168.1.10
```

On Kubernetes set `securityContext.capabilities: {drop: [ALL], add: [NET_BIND_SERVICE, NET_RAW]}` and use `/healthz` for the liveness and readiness probes. If you use `--tftpPort` above 1024 and always pass the MAC in the URL, no capabilities are required.

## Examples

[Example ignition configs and Kubernetes manifest](examples/README.md)

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

### Kubernetes

[Example deployment](examples/k8s.yaml)

Booty in `kube-system` on the control-plane node with `hostNetwork`, `--profile=kubeadm-worker --kubeadmJoin=auto`, the ServiceAccount and Roles the token minting needs, a ConfigMap with the site-only Butane, a Secret with the SSH keys mounted for `--sshAuthorizedKeysFile`, `/healthz` probes, `NET_BIND_SERVICE`+`NET_RAW` only, and two MetalLB Services sharing one VIP. Set `--serverIP` to the address booting machines actually reach.

## Development

```
make build      # builds the Web UI, then the Go binary with it embedded -> bin/booty
make run        # build + run against ./data with --debug
make test       # go test -race + Vitest
make lint       # golangci-lint + eslint + vue-tsc
make image      # multi-stage container build (VERSION/TIMESTAMP stamped into /info)
make ipxe       # rebuild boot/*.kpxe|*.efi from the pinned iPXE release in a container (only when bumping iPXE or editing boot/embed.ipxe)
```

The iPXE binaries in `boot/` are committed, so a plain `go build` needs no cross toolchain and works air-gapped. `make ipxe` (see [hack/build-ipxe.sh](hack/build-ipxe.sh)) rebuilds them reproducibly; verify with `cd boot && sha256sum -c SHA256SUMS`. Licensing details are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

The Web UI lives in `web/` (Vue 3 + Vite). `cd web && npm run dev` starts a dev server that proxies API calls to a Booty running on `localhost:8080` (override with `VITE_API_TARGET`). See [web/README.md](web/README.md).

## Additional Thoughts

**Why?**

I like treating (most of) my machines like cattle. This is an easier and more lightweight way to tackle network booting and patch management.

**Can you make it do X?**

Feature requests / optimizations / PRs are welcome! Feel free to ping me [@jeefy](https://twitter.com/jeefy) on Twitter.