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

Flags:
      --autoRegister string            Register unknown MACs on their first /booty.ipxe or /ignition.json fetch as this OS (flatcar, coreos or bluefin) instead of sending them to the brig; empty disables
      --bluefinRepo string             GitHub repository whose installer-v* releases provide the Bluefin Server PXE kernel, initrd and DDI (default "projectbluefin/server")
      --bluefinVersion string          Pin a specific Bluefin Server release (e.g. 26.08.0). When empty, tracks the newest installer-v* release
      --builtin string                 Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys), or 'none' to serve the user config as-is (default "hostname,update,booted,sshkeys")
      --clusterCADir string            Bring-your-own cluster CA directory for --controlPlane=managed (kubeadm: ca.crt/ca.key; k0s: also sa.key, sa.pub, etcd/ca.crt, etcd/ca.key), read-only; empty generates one under --dataDir/cluster/pki
      --clusterDistribution string     Kubernetes distribution of the cluster Booty provisions: 'kubeadm' or 'k0s' (Bluefin hosts support k0s only) (default "kubeadm")
      --cni string                     Network plugin installed from the first control plane: 'cilium', 'calico', 'flannel' or 'none' (default "cilium")
      --cniRelease string              Overrides the pinned release of the selected --cni (--cniVersion keeps meaning containernetworking/plugins)
      --cniVersion string              containernetworking/plugins release installed by the kubeadm-worker profile (default "v1.1.1")
      --containerdDisk string          Block device the kubeadm-worker profile formats (ext4, wiped on every boot) and mounts at /var/lib/containerd, e.g. /dev/sda; empty keeps containerd on the root filesystem
      --controlPlane string            Who runs the control plane: 'external' (join-only, today's behaviour) or 'managed' (Booty generates the cluster CA under --dataDir/cluster/ and renders the role: control-plane host) (default "external")
      --controlPlaneDisk string        Block device the managed kubeadm control plane formats once (ext4, label booty-cp, never wiped) and keeps /etc/kubernetes, /var/lib/etcd and /var/lib/kubelet on, e.g. /dev/vda; required for a role: control-plane host on PXE-booted Flatcar/CoreOS and must differ from --containerdDisk
      --controlPlaneEndpoint string    host[:port] every node uses for the API server (VIP or DNS name for HA); with --controlPlane=managed it defaults to the single role: control-plane host's IP
      --coreOSArchitecture string      Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string           CoreOS channel to look for updates (default "stable")
      --crictlVersion string           cri-tools release installed by the kubeadm-worker profile; defaults to the --k8sVersion minor with patch 0 (cri-tools tags once per minor, e.g. v1.34.0)
      --dataDir string                 Directory to store stateful data (default "/data")
      --debug                          Enable debug logging
      --doInstallClearOn string        When to clear a host's pending doInstall: 'ignition' (first Ignition fetch), 'booted' (only on POST /booted from the installed system) or 'next-boot' (Bluefin: the first /booty.ipxe fetch at least --installMinDuration after the install stanza was served; other OSes behave like 'booted') (default "ignition")
      --flatcarArchitecture string     Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string          Flatcar channel to look for updates (default "stable")
      --flatcarVersion string          Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel
      --githubToken string             GitHub token sent as a bearer token to the releases API (raises the unauthenticated 60 requests/hour limit); no scopes needed
  -h, --help                           help for booty
      --hostnameTemplate string        Go template for auto-registered hostnames; fields: .MAC, .MACSuffix (last 3 bytes hex), .MACFlat (12 hex), .IP (default "node-{{ .MACSuffix }}")
      --httpPort int                   Port to use for the HTTP server (default 8080)
      --installMinDuration duration    Minimum time between serving a Bluefin install stanza and the re-PXE that counts as 'install finished' for --doInstallClearOn=next-boot; earlier re-PXEs keep doInstall (default 3m0s)
      --joinString string              The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)
      --joinStringFile string          File holding the kubeadm join string (e.g. a mounted Secret); re-read on every render and wins over --joinString
      --joinTokenTTL duration          Lifetime of bootstrap tokens minted with --kubeadmJoin=auto; expired ones are deleted on the --updateSchedule tick (default 1h0m0s)
      --k0sTokenFile string            File holding a pre-made k0s worker join token for an external k0s control plane
      --k8sVersion string              Kubernetes release installed by the kubeadm-worker profile (default "v1.34.3")
      --kubeadmJoin string             Where the kubeadm join string comes from: 'static' (--joinString/--joinStringFile) or 'auto' (mint a short-lived bootstrap token through the Kubernetes API on every boot: in-cluster, via --kubeconfig, or with Booty's own CA when --controlPlane=managed) (default "static")
      --kubeconfig string              Kubeconfig for minting --kubeadmJoin=auto tokens against an external kubeadm control plane from outside the cluster
      --kubeletUnitsURL string         Base URL the kubeadm-worker profile fetches kubelet/kubelet.service and kubeadm/10-kubeadm.conf from (pin or mirror it) (default "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest")
      --ociGC                          Delete unreferenced OCI blobs from the local registry after a fully successful image sync (default true)
      --ociGCEmpty                     Allow blob GC to wipe the whole OCI blob cache when no registered host references an ostree image
      --podCIDR string                 Pod network CIDR of the cluster (default "10.244.0.0/16")
      --profile string                 Node profile appended to the builtin Ignition fragment for flatcar/coreos hosts: '' or 'kubeadm-worker' (CNI plugins, kubeadm/kubelet/kubectl/crictl, kubelet units, kubeadm join on every boot)
      --proxyDHCP                      EXPERIMENTAL: answer PXE clients as a ProxyDHCP server (UDP 67 + 4011) so the network's DHCP server needs no next-server/filename
      --proxyDHCPListen string         IP or interface name the ProxyDHCP server binds to (default all interfaces)
      --proxyDHCPRelay                 Answer relayed PXE requests (giaddr set) on the ProxyDHCP server
      --serverHttpPort int             HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort
      --serverIP string                IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)
      --serviceCIDR string             Service network CIDR of the cluster (default "10.96.0.0/12")
      --sshAuthorizedKeys strings      SSH public key added to the 'core' user by the sshkeys builtin (repeatable)
      --sshAuthorizedKeysFile string   File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin
      --tftpBlockSize int              TFTP block size to negotiate with clients (default 1468)
      --tftpPort int                   UDP port to use for the TFTP server (default 69)
      --updateSchedule string          Cron schedule for the Flatcar/CoreOS/Bluefin version checks and OSTree image sync (default "*/5 * * * *")
      --webDir string                  Directory with the built Web UI, used when no UI is embedded in the binary (default "./web/dist")

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

* iPXE boot (BIOS and x86-64 UEFI) into the latest Flatcar-Linux or CoreOS, and UEFI installs of Bluefin Server
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux, CoreOS and the Bluefin Server releases
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Configuration view (`/config`: every effective setting with its source, secrets redacted) and a Butane template editor with Butane validation, read-only aware for GitOps/ConfigMap mounts (see [Configuration view and editor](#configuration-view-and-editor))
* Builtin Ignition fragment (hostname, SSH keys, update timer, install-complete callback) merged into every host's config -- your Butane only carries what is specific to your fleet
* Fleet status: every host reports what it is running and whether a reboot is pending (`/update-check`, `/info`)
* `--profile=kubeadm-worker`: the whole Kubernetes worker setup (CNI, kubeadm/kubelet/kubectl/crictl, kubelet units, `kubeadm join` on every boot) as a versioned, embedded Ignition fragment
* `--kubeadmJoin=auto`: a fresh, one-hour kubeadm bootstrap token per boot, minted through the Kubernetes API -- no long-lived token on the boot network
* `--controlPlane=managed`: Booty generates the cluster CA and renders a whole kubeadm control plane (persistent state on `--controlPlaneDisk`, CNI installed from it) so a cluster boots from bare metal with nothing but Booty (see [Cluster bootstrap](#cluster-bootstrap))
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered), or are registered automatically with `--autoRegister` (see [Auto-registration](#auto-registration))
* `booty init` scaffolds a data directory with a commented starter Butane template and prints the DHCP settings
* Support for different operating systems and ignition files per machine
* Bluefin Server: unattended disk-image install with per-host hostname/SSH keys delivered as systemd credentials, no Butane needed (see [Bluefin Server](#bluefin-server))
* **EXPERIMENTAL**: Support for per-ostree images per machine (in conjunction with [ignition rebase scripts](examples/bazzite.but))
  * Auto-caches OCI images used for hosts (and has a page listing cached artifacts)
  * When "Install" is set to Y, it auto-flips to N once the host fetches its Ignition config (i.e. the installer has started)
* Self-contained binary: the iPXE bootloaders (`undionly.kpxe`, `ipxe.efi`, `snponly.efi`) and the Web UI are embedded, so it starts without network access
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
4. The OS fetches `http://<serverIP>/ignition.json?mac=<mac>`. For a registered host that is a tiny wrapper whose `ignition.config.merge` points at two children: Booty's builtin fragment (`/ignition/builtin.json`) and the host's Butane template rendered to Ignition (`/ignition/user.json`; variables: `.Hostname`, `.ServerIP`, `.JoinString`, `.OSTreeImage`). Ignition fetches and merges them itself, later entries winning, so your template overrides the builtin (and the `--profile` appended to it). The wrapper fetch records `booted`/`ip` for the host and, by default, clears a pending `doInstall`; the child fetches have no side effects. With `--doInstallClearOn=booted` the flag instead stays set until the installed system calls `POST http://<serverIP>/booted?mac=<mac>` (the builtin `booty-booted.service` does exactly that), so a failed install keeps the host in install mode. `--doInstallClearOn=next-boot` is for Bluefin Server, which never fetches Ignition; see [Bluefin Server](#bluefin-server). Add `&preview=1` (the UI does) to look at a config without recording a boot. Unregistered hosts receive an Ignition config whose only unit reboots the machine (the "brig").
5. Kernel/initrd/rootfs are served from `/data/`. Flatcar artifacts live in `data/flatcar/<version>/` behind symlinks at the old paths, so the kernel and initrd always come from the same release and updates are atomic. Bluefin Server releases live in `data/bluefin/<version>/` behind a `bluefin/current` symlink.

Bluefin Server hosts skip step 4: there is no Ignition. The installer is booted from the iPXE menu, fetches the disk image and the host's [credentials bundle](#bluefin-server) from Booty, and the installed system PXEs again and is told to boot from disk.

`/booty.ipxe` and `/ignition.json` fall back to an ARP lookup of the requesting IP when called without `?mac=`; the shipped scripts always pass it.

### Auto-registration

By default an unknown MAC gets the menu and the brig until you register it (in the UI, or `POST /register`). With `--autoRegister=flatcar|coreos|bluefin` Booty instead registers the host itself the first time it fetches `/booty.ipxe` or `/ignition.json`: it is stored with that OS, the requesting IP and a hostname rendered from `--hostnameTemplate`, logged as `Auto-registered unknown host`, and immediately served the real boot script and Ignition -- the machine never sees the brig and never appears under "Unknown hosts". Only the boot path does this; `/hosts`, `/ignition/user.json`, `/ignition/builtin.json` and `/update-check` still answer 404 for unregistered MACs and never create hosts.

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
* `/ignition/builtin.json` (spec 3.4.0, understood by Flatcar >= 3760 and Fedora CoreOS) carries, selectable with `--builtin` (default `hostname,update,booted,sshkeys`; `none` disables composition and `/ignition.json` serves your config verbatim, exactly as before):
  * `hostname` -- `/etc/hostname` (0644) with the hostname from the hardware database. Skipped when the host has no hostname.
  * `sshkeys` -- `passwd.users[core].sshAuthorizedKeys` from `--sshAuthorizedKeysFile` (one key per line, `#` comments ignored) and/or repeated `--sshAuthorizedKeys`. Skipped when there are no keys.
  * `booted` -- `booty-booted.service` (oneshot, enabled) that POSTs `/booted?mac=<mac>` once the installed system is up.
  * `update` -- `/opt/booty/update-check` (0755, inline, nothing fetched over HTTP at run time) plus `booty-update.service` and `booty-update.timer` (`OnCalendar=*:0/10`, `RandomizedDelaySec=60`). The script reads `/etc/os-release` (and `rpm-ostree status` on OSTree systems), calls `GET /update-check`, and touches or removes `/var/run/reboot-required` for kured. It fails closed: when Booty is unreachable or answers with anything but `rebootRequired:true|false`, the reboot flag is left alone.
* `/ignition/user.json` is your Butane template rendered and translated to Ignition -- the same bytes `/ignition.json` used to return. When neither `--ignitionFile` nor a per-host `ignitionFile` is set and `config/ignition.yaml` does not exist, Booty renders an embedded minimal template (hostname only) so a fresh data directory boots without any Butane at all. A per-host `ignitionFile` that is missing is still a 500 (that is a configuration error).
* Previews (`preview=1`, no side effects): `part=user` and `part=builtin` return the children, `part=merged` returns a pretty-printed **server-side** merge (both children upconverted to spec 3.5, builtin as parent, user as child) so you can see what the node will end up with. This is display-only; the node always performs its own merge.

## kubeadm worker profile

`--profile=kubeadm-worker` turns a registered `flatcar` or `coreos` host into a stateless Kubernetes worker without any Kubernetes-specific Butane. Booty appends the profile to `/ignition/builtin.json` after the builtin pieces, so the merge order on the node is **builtin -> profile -> user** and your template still overrides anything by path or unit name. `bluefin` hosts are skipped (logged at debug level): Bluefin Server is provisioned through systemd credentials, not Ignition, and ships k0s itself.

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

  **Cleanup**: on every `--updateSchedule` tick Booty lists `kube-system` Secrets of type `bootstrap.kubernetes.io/token` and deletes the ones whose `description` starts with `booty:` and whose `expiration` has passed. Tokens it did not create are never touched.

  **Failure mode**: if minting fails (RBAC, API down, not running in a cluster) Booty logs an error, sets `X-Booty-Warning: kubeadm join token unavailable` on the `/ignition.json` response and still serves the config with an empty `JOIN_STRING`. The node boots but does not join; fix the cause and reboot it.

## Cluster bootstrap

Booty is growing from "join workers to a cluster you already have" into provisioning a whole cluster from PXE: a Booty-managed or external control plane, `kubeadm` or `k0s`, on Flatcar, Fedora CoreOS and Bluefin Server, with the CNI installed from the first control plane. The design and the slices it ships in are in [docs/plans/2026-09-26-cluster-bootstrap.md](docs/plans/2026-09-26-cluster-bootstrap.md). Shipped so far: the model, CA and tokens (H1) and the managed **kubeadm** control plane with its CNI (H2). k0s and the Bluefin controller follow in H3. The defaults (`kubeadm`, `external`) equal the join-only behaviour above, and a host without a `role` renders byte-identical to before -- except for the two deliberate worker changes below -- guarded by a golden test.

Every host carries a `role` (`control-plane` or `worker`, default worker; `POST /register`, `/hosts`, `/booty.json`, the UI host form). The flags under "Cluster" above are validated at startup; `GET /cluster` reports `{distribution, controlPlane, endpoint, cni, ready, readyAt, caFingerprint, hosts:[{mac,hostname,os,role,booted}], warnings}` with no key material.

### Managed kubeadm control plane

```
booty --controlPlane=managed --controlPlaneEndpoint=10.77.0.10 --controlPlaneDisk=/dev/vda \
      --kubeadmJoin=auto --cni=cilium --serverIP=10.77.0.1
```

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

**`--controlPlaneDisk` is required for a control plane on Flatcar or CoreOS.** Both run from RAM when PXE-booted, so without a disk etcd and every certificate would vanish on the first reboot. Booty formats the device **once** (ext4, label `booty-cp`, `wipe_filesystem: false` -- an existing `booty-cp` filesystem is reused, a foreign filesystem makes Ignition refuse to boot rather than wipe it), mounts it at `/var/lib/booty-cp` and bind-mounts `/etc/kubernetes`, `/var/lib/etcd` and `/var/lib/kubelet` from subdirectories of it (`etc-kubernetes.mount`, `var-lib-etcd.mount`, `var-lib-kubelet.mount`, ordered before the kubelet and `booty-k8s-init` with `RequiresMountsFor=`). A `booty-cp-seed.service` copies the Ignition-written `/etc/kubernetes/pki/ca.*` onto the disk before the bind mount, without overwriting what is already there. Rendering a Flatcar/CoreOS control plane without the flag is refused with HTTP 400 `control-plane host needs --controlPlaneDisk on a PXE-booted OS` (and a warning in `GET /cluster`); `--controlPlaneDisk` and `--containerdDisk` must be different devices (startup error). The disk is the cluster's state: wiping it is a cluster reset.

The control plane keeps kubeadm's default `node-role.kubernetes.io/control-plane:NoSchedule` taint; Booty does not untaint it. Cilium, Calico and flannel tolerate it, so the CNI comes up on a single node, but your own workloads run on workers. Remove the taint yourself if you want a one-node cluster.

**Workers** under `--controlPlane=managed` render today's `kubeadm-worker` profile (implied even without `--profile`) with two changes that also apply in external mode: `booty-k8s-join.service` has `Restart=on-failure RestartSec=30s`, so a worker that boots before the control plane simply retries, and `join.sh` exits 0 when `/etc/kubernetes/kubelet.conf` exists (a disk-installed node that already joined; a PXE worker never has it and re-joins on every boot as before). The join string is `kubeadm join <endpoint>:6443 --token <the persisted bootstrap token> --discovery-token-ca-cert-hash sha256:<from ca.crt>` -- the same token `kubeadm init` was given, so it works before the API exists. With `--kubeadmJoin=auto` Booty first tries to mint a one-hour token with an admin certificate from its own CA and falls back to that pre-generated string while the API is unreachable (logged at debug level, no `X-Booty-Warning`: that is the expected state during bootstrap). A `--joinString`/`--joinStringFile` set explicitly still wins in `static` mode. A `role: control-plane` host never receives worker join units, in either mode.

**Bluefin** hosts cannot run kubeadm (no containerd/kubelet in `/usr`): under `--controlPlane=managed --clusterDistribution=kubeadm` their Ignition endpoints answer HTTP 400 `unsupported distribution kubeadm for os bluefin` and `GET /cluster` warns; in external mode they keep getting the plain builtin fragment as before. Bluefin joins the matrix with k0s in H3.

### CNI

`--cni` picks the plugin `booty-cni-apply.service` installs from the control plane; `--cniRelease` overrides the pinned release (`--cniVersion` keeps meaning containernetworking/plugins). Pins checked 2026-09-25:

| `--cni` | Release | How it is installed |
|---|---|---|
| `cilium` (default) | [v1.20.2](https://github.com/cilium/cilium/releases/tag/v1.20.2) via [cilium-cli v0.20.1](https://github.com/cilium/cilium-cli/releases/tag/v0.20.1) (tarball sha256-verified against the published `.sha256sum`, unpacked to `/opt/bin/cilium`) | `cilium install --version v1.20.2 --set ipam.mode=kubernetes` (pod ranges come from kubeadm's node podCIDRs), then `cilium status --wait`. `--cniRelease` changes the Cilium version, not the CLI pin. |
| `calico` | [v3.32.2](https://github.com/projectcalico/calico/releases/tag/v3.32.2) | `kubectl apply --server-side` of `manifests/tigera-operator.yaml`, then an `Installation` with `ipPools[0].cidr=--podCIDR` (VXLANCrossSubnet, NAT outgoing) and the `APIServer` CR; waits for `calico-node`. |
| `flannel` | [v0.28.9](https://github.com/flannel-io/flannel/releases/tag/v0.28.9) | Downloads `kube-flannel.yml` from the release; when `--podCIDR` is not flannel's built-in `10.244.0.0/16` the script rewrites the single `"Network"` entry in `net-conf.json` and refuses (unit fails, retries) if the manifest no longer has exactly one. Then `kubectl apply` and waits for `kube-flannel-ds`. |
| `none` | -- | Nothing is installed; bring your own. |

**Trust model.** With `--controlPlane=managed`, `--dataDir/cluster/` holds the cluster CA and its private key, which is cluster-admin: whoever can read that directory (or the node Booty renders it onto) owns the cluster. Booty never serves it over `/data/`, `/config` or `/cluster`, but the control-plane host's Ignition (`/ignition/builtin.json?mac=<cp>`) necessarily carries `ca.key`, the bootstrap token and the certificate key, readable by anyone on the boot VLAN during that host's first boot. Keep the data directory as private as your kubeconfig, VLAN-isolate the control plane's first boot, or use `--controlPlane=external` and keep the CA elsewhere. The UI masks `PRIVATE KEY` blocks in preview panes; the API does not.

## Fleet status

`GET /update-check?mac=&os=&version=&image=&digest=` is what `booty-update-check` calls every 10 minutes. It answers `{"rebootRequired":bool,"running":"...","target":"...","reason":"..."}`:

* Flatcar (`os=flatcar`, from `/etc/os-release`): `rebootRequired` when the reported `VERSION_ID` differs from the Flatcar release Booty serves. Booty answers `false` while it has not downloaded a release yet.
* OSTree systems (`os=coreos`, including Universal Blue images): `rpm-ostree status` supplies the running image reference and digest. If the host's registered `ostreeImage` is cached in Booty's registry, `rebootRequired` is set when the cached digest differs from the running one. Plain Fedora CoreOS without an image falls back to comparing `OSTREE_VERSION` with the CoreOS release Booty serves.
* Bluefin Server (`os=bluefin`, or a host registered as `bluefin`): always `rebootRequired:false` with the reason `bluefin updates itself via systemd-sysupdate; re-PXE only re-images`. The check is still recorded (`running`, `lastCheck`) so the fleet view shows what the node runs.
* Booty never says `true` when it cannot determine the target; 400/404 answers make the client leave the reboot flag alone.

Each check is recorded on the host (`running`, `lastCheck`, `rebootPending`, visible in `/booty.json`, `/hosts` and the UI), rewritten at most once a minute when nothing changed. `GET /info` gains `"fleet":{"hosts":N,"pendingReboots":N}` so you can see at a glance how many nodes are waiting on kured.

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

[Bluefin Server](https://github.com/projectbluefin/server) is an image-based server OS (Flatcar LTS userland, systemd-boot/UKI, updates through systemd-sysupdate, k0s as a sysext). It is not installed through Ignition: a PXE-booted installer writes a signed disk image (DDI) to a disk and the machine reboots into it. Booty drives exactly that.

**UEFI only.** The image is a UKI booted by systemd-boot, so the machine must PXE in UEFI mode. Point the DHCP `filename` for architecture `00:07`/`00:09` at `ipxe.efi` (or `snponly.efi`) as in [Booting](#booting-dhcp-and-the-ipxe-bootloaders), or use [ProxyDHCP](#zero-touch-dhcp-proxydhcp), which picks the bootloader per architecture. A host that reaches the Bluefin menu from BIOS iPXE is told so and dropped to the iPXE shell.

### Artifacts

Booty tracks the `installer-v*` releases of `--bluefinRepo` (default `projectbluefin/server`) through the GitHub releases API (`api.github.com`, unauthenticated: 60 requests/hour, Booty asks once per `--updateSchedule` tick; `--githubToken`/`BOOTY_GITHUBTOKEN` is sent as a bearer token if you share the address with other API clients). The newest published, non-prerelease release that carries a PXE kernel, initrd and `SHA256SUMS` wins; `--bluefinVersion=26.08.0` pins one (pinned versions are fetched directly, without the API). Older `bluefin-server-v*` tags are ignored.

For the chosen release Booty downloads `SHA256SUMS`, then `bluefin-server-pxe-vmlinuz-<ver>`, `bluefin-server-pxe-initrd-<ver>.cpio.gz` and **one** `bluefin-server-ddi-*.raw.zst`, each verified against `SHA256SUMS`; a release may ship several DDIs (26.08.0 has a legacy `ddi-26.08.0` that is *not* in `SHA256SUMS` and the Flatcar-LTS-based `ddi-4593.2.5` that is), so Booty only considers DDIs listed in `SHA256SUMS`, prefers the one named after the release version and otherwise takes the last one listed -- the log line `Selected Bluefin artifacts` says which. Everything lands in `data/bluefin/<version>/` together with `SHA256SUMS` and a `manifest.json` (`version`, `vmlinuz`, `initrd`, `ddi`, `ddiSha256`); `data/bluefin/current` is repointed only after every file is on disk, older release directories are then pruned, and a manifest whose files went missing resets the version at start-up so the next tick re-downloads. About 0.9 GB per release. `/info` shows `"bluefin":{"version","pinnedVersion"}`, `/version.json` gains `bluefin` and `/version.txt` a `BLUEFIN_VERSION=` line.

### How an install works

Register the host with `os: bluefin`, optionally `installDisk: /dev/sda` (must start with `/dev/`, no whitespace or `..`; the installer picks the first writable disk when unset) and set `doInstall`. `/booty.ipxe` then renders a menu (5 s timeout, default `install` while `doInstall` is set, `run-from-disk` otherwise):

```
kernel http://<serverIP>/data/bluefin/current/bluefin-server-pxe-vmlinuz-26.08.0 systemd.unit=system-install.target console=tty0 console=ttyS0,115200 rw unattended \
  inst.ddi_url=http://<serverIP>/data/bluefin/current/bluefin-server-ddi-4593.2.5.raw.zst inst.ddi_sha256=<sha256 from SHA256SUMS> \
  inst.target_disk=/dev/sda inst.creds_url=http://<serverIP>/creds/<mac>.tar inst.creds_sha256=<sha256 of that tar>
initrd http://<serverIP>/data/bluefin/current/bluefin-server-pxe-initrd-26.08.0.cpio.gz
```

The installer downloads the DDI (plain HTTP is fine; `inst.ddi_sha256` is mandatory and verified by the installer), writes it to the disk and reboots. The firmware PXEs again; Booty must now answer "boot from disk", which the menu does (`exit` hands control back to the UEFI firmware, which continues with the disk). Until a release is cached the menu only offers *Boot from disk* / *iPXE shell* and prints `Bluefin artifacts not downloaded yet`.

Clearing `doInstall` after a Bluefin install: `--doInstallClearOn=ignition` never fires (no Ignition fetch); `booted` works once the credentials bundle is applied (its `booty-booted.service` POSTs `/booted`); **`next-boot`** needs nothing on the node: Booty records `installServedAt` whenever it serves the install stanza, and the first `/booty.ipxe` fetch from that MAC at least `--installMinDuration` (default `3m`) later clears `doInstall` and serves *Boot from disk*. A re-PXE sooner than that means the installer crashed, so the host keeps installing (and the timer restarts). For `flatcar`/`coreos` hosts `next-boot` behaves like `booted`.

### Credentials bundle

Per-host provisioning uses [systemd credentials](https://systemd.io/CREDENTIALS/): `*.cred` files in the ESP's `/loader/credentials/` are picked up by systemd-stub and handed to the booted UKI. Booty serves them as a tar at `GET /creds/<mac>.tar` (`application/x-tar`, 404 for unregistered MACs) with its digest at `GET /creds/<mac>.tar.sha256`. The tar is deterministic (fixed mtime, uid/gid 0, mode 0600, sorted names), so the `inst.creds_sha256` in the iPXE script always matches the bytes the installer downloads.

**Every credential is encrypted with the systemd "null" key**, exactly as `systemd-creds --with-key=null encrypt --name=<name>` would write it (AES-256-GCM under a fixed key, base64 text). This is not for secrecy -- the null key is public and adds no confidentiality -- but because systemd only imports `/loader/credentials/*.cred` as *encrypted* credentials (`/run/credentials/@encrypted`); plaintext files there make every `ImportCredential=` consumer fail with `Failed to set up credentials: Invalid argument`. Booty does this in Go, no `systemd-creds` binary needed at run time, and derives the IV from the credential name and contents so the bundle (and its digest) stays stable across restarts. The name embedded in each credential equals its file name without `.cred`, which systemd checks.

Null-key credentials are decrypted **only for `ImportCredential=` consumers** (`systemd-tmpfiles`, `systemd-sysusers`, the network generator, udev). PID 1 and the generators read credentials without allowing the null key, so `systemd.extra-unit.*`, `systemd.unit-dropin.*` and `system.hostname` are rejected with `Failed to read credential '...', ignoring: Operation not supported` (verified on Bluefin Server 26.08.0, systemd 257, no TPM, Secure Boot off). Everything Booty wants on the node is therefore delivered through `tmpfiles.extra`, which `systemd-tmpfiles-setup.service` applies on every boot.

The entries follow the same `--builtin` toggles as the Ignition fragment:

* `firstboot.hostname.cred` (`hostname`) -- for `systemd-firstboot` via `bluefin-firstboot-credentials.service`. The `installer-v26.08.0` image lacks the `systemd-firstboot` binary (the unit fails with `Failed at step EXEC spawning systemd-firstboot`), so this is a no-op there; the hostname is applied through `tmpfiles.extra` instead (below). Kept for images that ship the binary.
* `tmpfiles.extra.cred` (`hostname`, `sshkeys`, `booted`, `update`) -- `systemd-tmpfiles` rules for everything else:
  * `hostname`: `f+ /etc/hostname` with the hostname, plus `booty-hostname.service` (`WantedBy=sysinit.target`, `Before=network-pre.target`) which copies `/etc/hostname` into the kernel when they differ. PID 1 reads `/etc/hostname` before tmpfiles has written it, so on the very first boot the name is set by that unit -- and only after the daemon-reload below, so first-boot DHCP may still say `localhost`. From the second boot on PID 1 applies the file itself and the unit is a no-op.
  * `sshkeys`: `/home/core/.ssh/authorized_keys` from `--sshAuthorizedKeys`/`--sshAuthorizedKeysFile` (the `core` user exists and SSH is key-only, so without this nobody can log in).
  * `booted`: `booty-booted.service` with a `multi-user.target.wants/` symlink.
  * `update`: `/opt/booty/update-check`, `booty-update.service` and `booty-update.timer` with a `timers.target.wants/` symlink.

  Units are written as files under `/etc/systemd/system/` with the `.wants/` symlinks `systemctl enable` would have created, the same text the Ignition builtin ships. PID 1 has already computed the boot's job graph when tmpfiles runs, so on the **first boot** those symlinks alone leave the units loaded and enabled but never started. The image's own `k0s-first-boot.service` (every boot) ends with `systemctl daemon-reload` and `systemctl enable --now k0scontroller.service`; that job is enqueued after the reload, so the bundle also drops `k0scontroller.service.d/booty.conf` with `Wants=booty-hostname.service booty-booted.service booty-update.timer`, which starts them on the first boot (verified: `/booted` reported, timer active). On every later boot they load from `/etc` like any other unit and the drop-in is redundant.

Networking is left to the OS default (DHCP). The bundle holds public keys and unit files only, nothing secret -- the same material `/ignition/builtin.json` already exposes to the boot VLAN (see [Trust model](#trust-model)); the null-key encryption adds no confidentiality and is not meant to.

**Upstream dependency.** The Bluefin installer does not fetch credentials over the network yet; `inst.creds_url=`/`inst.creds_sha256=` are being proposed upstream (a tar of `*.cred` files unpacked into the new ESP's `/loader/credentials/`). Until that lands, installs complete but the node comes up with the default hostname and no SSH access, and only `--doInstallClearOn=next-boot` can clear `doInstall`. Booty already emits the arguments so nothing changes on your side once the installer supports them.

### Updates

Bluefin Server updates itself with systemd-sysupdate; Booty does not drive its reboots. `GET /update-check` from a `bluefin` host (or with `os=bluefin`) always answers `rebootRequired:false` with the reason `bluefin updates itself via systemd-sysupdate; re-PXE only re-images`, while still recording `running`/`lastCheck` for the fleet view. To re-image a host, set `doInstall` again and reboot it into PXE.

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

The last row is iPXE's own second-stage DHCP: once `undionly.kpxe`/`ipxe.efi` is running it repeats DHCP with user class `iPXE`. Handing it another iPXE binary would loop, so Booty gives it the `booty.ipxe` stub instead. iPXE resolves a bare filename against `tftp://${next-server}/`, and `${next-server}` falls back to the `siaddr` of the ProxyDHCP reply when the real DHCP server's `siaddr` is empty (iPXE keeps ProxyDHCP settings in a lower-priority `proxydhcp` settings block that is consulted whenever the primary DHCP settings lack a value), so this resolves to Booty. If your iPXE build has an embedded script the filename is ignored and the embedded script runs instead.

Ports 67 and 4011 are privileged, so the container needs `--network=host`/`hostNetwork: true` and `CAP_NET_BIND_SERVICE` (already granted in the examples above). `--proxyDHCPListen` takes an IPv4 address or an interface name; prefer the interface name on multi-homed hosts, because a socket bound to a unicast address does not receive the broadcast DHCPDISCOVERs on Linux. Only iPXE's `ipxe.efi` needs to be present in `--dataDir` for UEFI clients; `undionly.kpxe` and `booty.ipxe` are built in. For tests without root the two ports can be overridden with the environment variable `BOOTY_PROXYDHCPPORTS=1067,5011` (test-only, not a flag).

## Trust model

Booty is meant to run on a network you control. It has **no authentication**: anyone who can reach the HTTP port can register hosts, read any registered host's rendered Ignition (including the kubeadm join string and the builtin SSH keys) via `/ignition.json?mac=` or `/ignition/builtin.json?mac=`, download boot artifacts, and -- through the [configuration editor](#configuration-view-and-editor) -- read the effective configuration (`joinString` and `githubToken` redacted) and, unless the data directory is mounted read-only, rewrite the Butane template every host boots with. This is inherent to PXE -- the booting machine has no credentials yet -- so treat the boot VLAN like you treat your DHCP server.

With `--kubeadmJoin=static` the join string is whatever you configured, typically a never-expiring token that grants node-join to anyone who reads it. Prefer `--kubeadmJoin=auto`: each real boot gets its own token that expires after `--joinTokenTTL` (1 h by default) and is deleted afterwards, so what leaks over the boot VLAN is worth at most one hour of node-join; previews never mint. Booty's own credential for that is its service account, scoped by the Roles in [examples/k8s.yaml](examples/k8s.yaml) to creating/listing/deleting Secrets in `kube-system` and reading `cluster-info`.

What Booty does enforce: TFTP and HTTP file serving are confined to `--dataDir` (no path traversal, no directory listings), `hardware.json`, the version pin file, temp files, the OCI blob store and everything under `cluster/` (the cluster CA and bootstrap tokens, see [Cluster bootstrap](#cluster-bootstrap)) are never served over `/data/`, Ignition template paths from the hardware database must stay inside `--dataDir`, and all inputs (MACs, hostnames, OS names, roles, versions, `installDisk`) are validated. The Bluefin [credentials bundle](#credentials-bundle) at `/creds/<mac>.tar` carries the same SSH public keys and units as `/ignition/builtin.json`, readable by anyone on the boot VLAN (its null-key encryption is a systemd import requirement, not confidentiality), and nothing secret. `--autoRegister` widens this further; read [Auto-registration](#auto-registration) before turning it on.

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