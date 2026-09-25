# Booty

A simple iPXE server for booting Flatcar-Linux, CoreOS, and [Universal Blue](https://universal-blue.org) on BIOS and x86-64 UEFI machines.

```
> booty --help

Easy iPXE server for Flatcar, CoreOS, and more

Usage:
  booty [flags]
  booty [command]

Available Commands:
  init        Create a data directory with a starter Butane template and an empty hardware map

Flags:
      --autoRegister string            Register unknown MACs on their first /booty.ipxe or /ignition.json fetch as this OS (flatcar, coreos or ublue) instead of sending them to the brig; empty disables
      --builtin string                 Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys), or 'none' to serve the user config as-is (default "hostname,update,booted,sshkeys")
      --cniVersion string              containernetworking/plugins release installed by the kubeadm-worker profile (default "v1.1.1")
      --containerdDisk string          Block device the kubeadm-worker profile formats (ext4, wiped on every boot) and mounts at /var/lib/containerd, e.g. /dev/sda; empty keeps containerd on the root filesystem
      --coreOSArchitecture string      Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string           CoreOS channel to look for updates (default "stable")
      --crictlVersion string           cri-tools release installed by the kubeadm-worker profile; defaults to --k8sVersion
      --dataDir string                 Directory to store stateful data (default "/data")
      --debug                          Enable debug logging
      --doInstallClearOn string        When to clear a host's pending doInstall: 'ignition' (first Ignition fetch) or 'booted' (only on POST /booted from the installed system) (default "ignition")
      --flatcarArchitecture string     Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string          Flatcar channel to look for updates (default "stable")
      --flatcarVersion string          Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel
  -h, --help                           help for booty
      --hostnameTemplate string        Go template for auto-registered hostnames; fields: .MAC, .MACSuffix (last 3 bytes hex), .MACFlat (12 hex), .IP (default "node-{{ .MACSuffix }}")
      --httpPort int                   Port to use for the HTTP server (default 8080)
      --joinString string              The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)
      --joinStringFile string          File holding the kubeadm join string (e.g. a mounted Secret); re-read on every render and wins over --joinString
      --joinTokenTTL duration          Lifetime of bootstrap tokens minted with --kubeadmJoin=auto; expired ones are deleted on the --updateSchedule tick (default 1h0m0s)
      --k8sVersion string              Kubernetes release installed by the kubeadm-worker profile (default "v1.34.3")
      --kubeadmJoin string             Where the kubeadm join string comes from: 'static' (--joinString/--joinStringFile) or 'auto' (mint a short-lived bootstrap token through the Kubernetes API on every boot; in-cluster only) (default "static")
      --kubeletUnitsURL string         Base URL the kubeadm-worker profile fetches kubelet/kubelet.service and kubeadm/10-kubeadm.conf from (pin or mirror it) (default "https://raw.githubusercontent.com/kubernetes/release/master/cmd/krel/templates/latest")
      --ociGC                          Delete unreferenced OCI blobs from the local registry after a fully successful image sync (default true)
      --ociGCEmpty                     Allow blob GC to wipe the whole OCI blob cache when no registered host references an ostree image
      --profile string                 Node profile appended to the builtin Ignition fragment for flatcar/coreos hosts: '' or 'kubeadm-worker' (CNI plugins, kubeadm/kubelet/kubectl/crictl, kubelet units, kubeadm join on every boot)
      --proxyDHCP                      EXPERIMENTAL: answer PXE clients as a ProxyDHCP server (UDP 67 + 4011) so the network's DHCP server needs no next-server/filename
      --proxyDHCPListen string         IP or interface name the ProxyDHCP server binds to (default all interfaces)
      --proxyDHCPRelay                 Answer relayed PXE requests (giaddr set) on the ProxyDHCP server
      --serverHttpPort int             HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort
      --serverIP string                IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)
      --sshAuthorizedKeys strings      SSH public key added to the 'core' user by the sshkeys builtin (repeatable)
      --sshAuthorizedKeysFile string   File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin
      --tftpBlockSize int              TFTP block size to negotiate with clients (default 1468)
      --tftpPort int                   UDP port to use for the TFTP server (default 69)
      --updateSchedule string          Cron schedule for the Flatcar/CoreOS version checks and OSTree image sync (default "*/5 * * * *")
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

* iPXE boot (BIOS and x86-64 UEFI) into the latest Flatcar-Linux or CoreOS
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux and CoreOS
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Builtin Ignition fragment (hostname, SSH keys, update timer, install-complete callback) merged into every host's config -- your Butane only carries what is specific to your fleet
* Fleet status: every host reports what it is running and whether a reboot is pending (`/update-check`, `/info`)
* `--profile=kubeadm-worker`: the whole Kubernetes worker setup (CNI, kubeadm/kubelet/kubectl/crictl, kubelet units, `kubeadm join` on every boot) as a versioned, embedded Ignition fragment
* `--kubeadmJoin=auto`: a fresh, one-hour kubeadm bootstrap token per boot, minted through the Kubernetes API -- no long-lived token on the boot network
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered), or are registered automatically with `--autoRegister` (see [Auto-registration](#auto-registration))
* `booty init` scaffolds a data directory with a commented starter Butane template and prints the DHCP settings
* Support for different operating systems and ignition files per machine
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
3. `/booty.ipxe` looks the MAC up in the hardware database and renders the boot script for that host's OS (`flatcar`, `coreos` or `ublue`). Unregistered hosts get an interactive menu (boot from disk / reboot) and show up under "Unknown hosts" in the UI so you can register them with one click -- unless `--autoRegister` is set, in which case they are registered on the spot (see [Auto-registration](#auto-registration)).
4. The OS fetches `http://<serverIP>/ignition.json?mac=<mac>`. For a registered host that is a tiny wrapper whose `ignition.config.merge` points at two children: Booty's builtin fragment (`/ignition/builtin.json`) and the host's Butane template rendered to Ignition (`/ignition/user.json`; variables: `.Hostname`, `.ServerIP`, `.JoinString`, `.OSTreeImage`). Ignition fetches and merges them itself, later entries winning, so your template overrides the builtin (and the `--profile` appended to it). The wrapper fetch records `booted`/`ip` for the host and, by default, clears a pending `doInstall`; the child fetches have no side effects. With `--doInstallClearOn=booted` the flag instead stays set until the installed system calls `POST http://<serverIP>/booted?mac=<mac>` (the builtin `booty-booted.service` does exactly that), so a failed install keeps the host in install mode. Add `&preview=1` (the UI does) to look at a config without recording a boot. Unregistered hosts receive an Ignition config whose only unit reboots the machine (the "brig").
5. Kernel/initrd/rootfs are served from `/data/`. Flatcar artifacts live in `data/flatcar/<version>/` behind symlinks at the old paths, so the kernel and initrd always come from the same release and updates are atomic.

`/booty.ipxe` and `/ignition.json` fall back to an ARP lookup of the requesting IP when called without `?mac=`; the shipped scripts always pass it.

### Auto-registration

By default an unknown MAC gets the menu and the brig until you register it (in the UI, or `POST /register`). With `--autoRegister=flatcar|coreos|ublue` Booty instead registers the host itself the first time it fetches `/booty.ipxe` or `/ignition.json`: it is stored with that OS, the requesting IP and a hostname rendered from `--hostnameTemplate`, logged as `Auto-registered unknown host`, and immediately served the real boot script and Ignition -- the machine never sees the brig and never appears under "Unknown hosts". Only the boot path does this; `/hosts`, `/ignition/user.json`, `/ignition/builtin.json` and `/update-check` still answer 404 for unregistered MACs and never create hosts.

`--hostnameTemplate` is a Go `text/template` (default `node-{{ .MACSuffix }}`) with these fields:

| Field | Example for `aa:bb:cc:dd:ee:ff` from `192.168.1.57` |
|---|---|
| `.MAC` | `aa:bb:cc:dd:ee:ff` (not a valid hostname on its own) |
| `.MACSuffix` | `ddeeff` |
| `.MACFlat` | `aabbccddeeff` |
| `.IP` | `192.168.1.57` |

The result must be an RFC 1123 label or dotted name (`[a-z0-9-]`, no leading/trailing hyphen, at most 253 characters); Booty checks at startup that the template parses and renders a valid name for a dummy MAC, and rejects the flag otherwise. `/register` applies the same rule to a non-empty `hostname`. Hostnames are not required to be unique -- a template like `worker` registers every machine as `worker` with only a warning in the log -- so keep a MAC-derived field in it.

**Trust model caveat.** Auto-registration turns "any device on the boot VLAN can PXE boot" into "any device on the boot VLAN becomes a registered host that receives your Ignition config" (including `--joinString` and the builtin SSH keys, which unregistered hosts can already read; see [Trust model](#trust-model)). For a kubeadm cluster this also means a stray or wrongly named machine joins as a Node; a hostname collision creates a duplicate Node object or hijacks an existing one. The brig is the safer default; turn auto-registration on when the boot network is closed and you want zero-touch provisioning. Auto-registered hosts start with `doInstall` unset, so for uBlue/CoreOS installs you still flip it in the UI.

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

`--profile=kubeadm-worker` turns a registered `flatcar` or `coreos` host into a stateless Kubernetes worker without any Kubernetes-specific Butane. Booty appends the profile to `/ignition/builtin.json` after the builtin pieces, so the merge order on the node is **builtin -> profile -> user** and your template still overrides anything by path or unit name. `ublue` hosts are skipped (logged at debug level): they are rpm-ostree desktops.

What the profile installs, all as inline files under `/opt/booty/` (0755, embedded as `data:` URLs -- nothing is fetched from Booty at run time) and a chain of enabled oneshot units, each `Requires=`/`After=` the previous one:

| Unit | Script | Does |
|---|---|---|
| `booty-cni-install.service` (`Wants=network-online.target`) | `cni.sh` | Unpacks [containernetworking/plugins](https://github.com/containernetworking/plugins) `--cniVersion` into `/opt/bin/` |
| `booty-kube-tools.service` | `kube-tools.sh` | **Flatcar**: downloads the `--k8sVersion` `kubeadm`/`kubelet`/`kubectl` static binaries from `dl.k8s.io` and `crictl` `--crictlVersion` (default = `--k8sVersion`) from [cri-tools](https://github.com/kubernetes-sigs/cri-tools) into `/opt/bin/`. **Fedora CoreOS** (`VARIANT_ID=fedora`): writes the `pkgs.k8s.io` repo for the matching minor, `dnf install`s the pinned RPMs plus `cri-tools`, excludes them from further updates and symlinks them into `/opt/bin/`. Both create `/etc/kubernetes/manifests`. |
| `booty-kubelet-setup.service` | `systemd.sh` | Fetches `kubelet/kubelet.service` and `kubeadm/10-kubeadm.conf` from `--kubeletUnitsURL` (default: the `master` templates in [kubernetes/release](https://github.com/kubernetes/release/tree/master/cmd/krel/templates/latest); pin or mirror it if you want reproducible boots), rewrites `/usr/bin` to `/opt/bin`, sets `KUBELET_EXTRA_ARGS=--cgroup-driver=systemd --fail-swap-on=false`, enables and starts the kubelet |
| `booty-k8s-join.service` | `join.sh` | `Environment="JOIN_STRING=..."`; loads `br_netfilter`, sets the bridge/forwarding sysctls, runs `kubeadm reset -f` and then the join string. An empty `JOIN_STRING` logs a message and exits 0, so the node still boots. |

`--containerdDisk=/dev/sda` additionally adds an ext4 filesystem (`wipe_filesystem: true`, label `ssd`) mounted at `/var/lib/containerd` through `var-lib-containerd.mount` and a `containerd.service` drop-in that `Requires=`/`After=` the mount -- an ephemeral image cache that is wiped on every boot together with the rest of the stateless worker.

Flags: `--profile` (`""` or `kubeadm-worker`, validated at start-up), `--k8sVersion` (default `v1.34.3`), `--cniVersion` (default `v1.1.1`), `--crictlVersion` (default `--k8sVersion`), `--containerdDisk` (default none), `--kubeletUnitsURL`. `part=merged` previews show the profile merged with your template.

## Automatic join tokens

`--kubeadmJoin` decides where `{{ .JoinString }}` and the profile's `JOIN_STRING` come from:

* `static` (default): `--joinString`, or the trimmed contents of `--joinStringFile` when set (re-read on every render, so a rotated mounted Secret is picked up; the file wins over the flag).
* `auto`: Booty mints a **bootstrap token per boot** through the Kubernetes API it runs in. When a registered `flatcar`/`coreos` host fetches `/ignition.json` (the real fetch, not `preview=1`), Booty
  1. reads `kube-public/cluster-info` (cached 10 minutes) for the **external** API endpoint (`clusters[0].cluster.server`) and the CA, and computes the discovery hash `sha256(SubjectPublicKeyInfo)`;
  2. creates the Secret `kube-system/bootstrap-token-<id>` (type `bootstrap.kubernetes.io/token`, 6-char id + 16-char secret from `crypto/rand`, `usage-bootstrap-authentication`/`-signing`, `auth-extra-groups: system:bootstrappers:kubeadm:default-node-token`, `expiration` = now + `--joinTokenTTL` (default `1h`), `description: "booty: <hostname> <mac>"`);
  3. renders `kubeadm join <endpoint> --token <id>.<secret> --discovery-token-ca-cert-hash sha256:<hex>` and caches it per MAC for `ttl/2`. The `builtin.json`/`user.json` children Ignition fetches seconds later reuse the cached value (and mint if the cache is cold, e.g. after a Booty restart); retries within a boot do not churn tokens; previews only ever show the cache.

  Booty talks to the API server in-cluster only: `KUBERNETES_SERVICE_HOST`/`_PORT`, the service account token from `/var/run/secrets/kubernetes.io/serviceaccount/token` (re-read per request, it rotates) and the CA from `.../ca.crt`; 10 s timeouts; no client-go, plain `net/http`. RBAC needed (see [examples/k8s.yaml](examples/k8s.yaml)): a Role in `kube-system` on `secrets` with `create`, `list`, `delete`, `get`, and a Role in `kube-public` on `configmaps` named `cluster-info` with `get`.

  **Cleanup**: on every `--updateSchedule` tick Booty lists `kube-system` Secrets of type `bootstrap.kubernetes.io/token` and deletes the ones whose `description` starts with `booty:` and whose `expiration` has passed. Tokens it did not create are never touched.

  **Failure mode**: if minting fails (RBAC, API down, not running in a cluster) Booty logs an error, sets `X-Booty-Warning: kubeadm join token unavailable` on the `/ignition.json` response and still serves the config with an empty `JOIN_STRING`. The node boots but does not join; fix the cause and reboot it.

## Fleet status

`GET /update-check?mac=&os=&version=&image=&digest=` is what `booty-update-check` calls every 10 minutes. It answers `{"rebootRequired":bool,"running":"...","target":"...","reason":"..."}`:

* Flatcar (`os=flatcar`, from `/etc/os-release`): `rebootRequired` when the reported `VERSION_ID` differs from the Flatcar release Booty serves. Booty answers `false` while it has not downloaded a release yet.
* OSTree systems (`os=coreos`, uBlue images): `rpm-ostree status` supplies the running image reference and digest. If the host's registered `ostreeImage` is cached in Booty's registry, `rebootRequired` is set when the cached digest differs from the running one. Plain Fedora CoreOS without an image falls back to comparing `OSTREE_VERSION` with the CoreOS release Booty serves.
* Booty never says `true` when it cannot determine the target; 400/404 answers make the client leave the reboot flag alone.

Each check is recorded on the host (`running`, `lastCheck`, `rebootPending`, visible in `/booty.json`, `/hosts` and the UI), rewritten at most once a minute when nothing changed. `GET /info` gains `"fleet":{"hosts":N,"pendingReboots":N}` so you can see at a glance how many nodes are waiting on kured.

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

Booty is meant to run on a network you control. It has **no authentication**: anyone who can reach the HTTP port can register hosts, read any registered host's rendered Ignition (including the kubeadm join string and the builtin SSH keys) via `/ignition.json?mac=` or `/ignition/builtin.json?mac=`, and download boot artifacts. This is inherent to PXE -- the booting machine has no credentials yet -- so treat the boot VLAN like you treat your DHCP server.

With `--kubeadmJoin=static` the join string is whatever you configured, typically a never-expiring token that grants node-join to anyone who reads it. Prefer `--kubeadmJoin=auto`: each real boot gets its own token that expires after `--joinTokenTTL` (1 h by default) and is deleted afterwards, so what leaks over the boot VLAN is worth at most one hour of node-join; previews never mint. Booty's own credential for that is its service account, scoped by the Roles in [examples/k8s.yaml](examples/k8s.yaml) to creating/listing/deleting Secrets in `kube-system` and reading `cluster-info`.

What Booty does enforce: TFTP and HTTP file serving are confined to `--dataDir` (no path traversal, no directory listings), `hardware.json`, the version pin file, temp files and the OCI blob store are never served over `/data/`, Ignition template paths from the hardware database must stay inside `--dataDir`, and all inputs (MACs, hostnames, OS names, versions) are validated. `--autoRegister` widens this further; read [Auto-registration](#auto-registration) before turning it on.

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