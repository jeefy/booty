# Booty

A simple (i)PXE Server for booting Flatcar-Linux, CoreOS, and [Universal Blue](https://universal-blue.org)

```
> booty --help

Easy iPXE server for Flatcar, CoreOS, and more

Usage:
  booty [flags]

Flags:
      --builtin string                 Comma separated builtin Ignition fragments merged into every registered host's config (hostname, update, booted, sshkeys), or 'none' to serve the user config as-is (default "hostname,update,booted,sshkeys")
      --coreOSArchitecture string      Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string           CoreOS channel to look for updates (default "stable")
      --dataDir string                 Directory to store stateful data (default "/data")
      --debug                          Enable debug logging
      --doInstallClearOn string        When to clear a host's pending doInstall: 'ignition' (first Ignition fetch) or 'booted' (only on POST /booted from the installed system) (default "ignition")
      --flatcarArchitecture string     Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string          Flatcar channel to look for updates (default "stable")
      --flatcarVersion string          Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel
  -h, --help                           help for booty
      --httpPort int                   Port to use for the HTTP server (default 8080)
      --joinString string              The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)
      --ociGC                          Delete unreferenced OCI blobs from the local registry after a fully successful image sync (default true)
      --ociGCEmpty                     Allow blob GC to wipe the whole OCI blob cache when no registered host references an ostree image
      --serverHttpPort int             HTTP port clients use to reach Booty when it differs from --httpPort (port mapping); 0 means same as --httpPort
      --serverIP string                IP address that clients can connect to; autodetected from the default route when empty (set explicitly behind a VIP/NAT)
      --sshAuthorizedKeys strings      SSH public key added to the 'core' user by the sshkeys builtin (repeatable)
      --sshAuthorizedKeysFile string   File with SSH public keys (one per line) added to the 'core' user by the sshkeys builtin
      --tftpBlockSize int              TFTP block size to negotiate with clients (default 1468)
      --tftpPort int                   UDP port to use for the TFTP server (default 69)
      --updateSchedule string          Cron schedule for the Flatcar/CoreOS version checks and OSTree image sync (default "*/5 * * * *")
      --webDir string                  Directory with the built Web UI, used when no UI is embedded in the binary (default "./web/dist")
```

Every flag can also be set through the environment as `BOOTY_<FLAGNAME>` (upper-cased, e.g. `BOOTY_SERVERIP=192.168.1.10`, `BOOTY_JOINSTRING=...`). The older `IGNITION_FILE`, `HARDWARE_MAP`, `FLATCAR_VERSION_PIN`, `DEPS_PXELINUX_URL` and `DEPS_LDLINUX_URL` names still work.

`--serverIP` is the address booting machines use to reach Booty. When left empty it is autodetected at startup from the default route (logged as `serverIP autodetected`). Set it explicitly whenever that address is not what clients should use -- behind a MetalLB/keepalived VIP, a NAT or a hostPort mapping the node IP is the wrong answer. `--serverHttpPort` only matters when clients reach Booty on a different port than it listens on (port mapping); it defaults to `--httpPort`, and `:80` is omitted from generated URLs.

## Features

* (i)PXE boot into the latest Flatcar-Linux or CoreOS
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux and CoreOS
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
* Builtin Ignition fragment (hostname, SSH keys, update timer, install-complete callback) merged into every host's config -- your Butane only carries what is specific to your fleet
* Fleet status: every host reports what it is running and whether a reboot is pending (`/update-check`, `/info`)
* Unrecognized MAC addresses go into the brig (boot loop till the MAC is registered)
* Support for different operating systems and ignition files per machine
* **EXPERIMENTAL**: Support for per-ostree images per machine (in conjunction with [ignition rebase scripts](examples/bazzite.but))
  * Auto-caches OCI images used for hosts (and has a page listing cached artifacts)
  * When "Install" is set to Y, it auto-flips to N once the host fetches its Ignition config (i.e. the installer has started)
* Self-contained binary: `undionly.kpxe` and the Web UI are embedded, so it starts without network access
* `/healthz` for liveness/readiness probes; graceful shutdown on SIGTERM

## How a host boots

1. DHCP hands the machine `next-server` = Booty and `filename` = `undionly.kpxe` (iPXE) or `pxelinux.0` (legacy PXE).
2. iPXE fetches `booty.ipxe` over TFTP. That file is only a stub that chains to `http://<serverIP>/booty.ipxe?mac=${mac}` -- iPXE fills in its own MAC, so identification does not depend on ARP working across routers.
3. `/booty.ipxe` looks the MAC up in the hardware database and renders the boot script for that host's OS (`flatcar`, `coreos` or `ublue`). Unregistered hosts get an interactive menu (boot from disk / reboot) and show up under "Unknown hosts" in the UI so you can register them with one click.
4. The OS fetches `http://<serverIP>/ignition.json?mac=<mac>`. For a registered host that is a tiny wrapper whose `ignition.config.merge` points at two children: Booty's builtin fragment (`/ignition/builtin.json`) and the host's Butane template rendered to Ignition (`/ignition/user.json`; variables: `.Hostname`, `.ServerIP`, `.JoinString`, `.OSTreeImage`). Ignition fetches and merges them itself, later entries winning, so your template overrides the builtin. The wrapper fetch records `booted`/`ip` for the host and, by default, clears a pending `doInstall`; the child fetches have no side effects. With `--doInstallClearOn=booted` the flag instead stays set until the installed system calls `POST http://<serverIP>/booted?mac=<mac>` (the builtin `booty-booted.service` does exactly that), so a failed install keeps the host in install mode. Add `&preview=1` (the UI does) to look at a config without recording a boot. Unregistered hosts receive an Ignition config whose only unit reboots the machine (the "brig").
5. Kernel/initrd/rootfs are served from `/data/`. Flatcar artifacts live in `data/flatcar/<version>/` behind symlinks at the old paths, so the kernel and initrd always come from the same release and updates are atomic.

Legacy PXE clients ask for `pxelinux.cfg/01-<mac>` before `pxelinux.cfg/default`; Booty uses that MAC the same way.

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
  * `update` -- `/usr/local/bin/booty-update-check` (0755, inline, nothing fetched over HTTP at run time) plus `booty-update.service` and `booty-update.timer` (`OnCalendar=*:0/10`, `RandomizedDelaySec=60`). The script reads `/etc/os-release` (and `rpm-ostree status` on OSTree systems), calls `GET /update-check`, and touches or removes `/var/run/reboot-required` for kured. It fails closed: when Booty is unreachable or answers with anything but `rebootRequired:true|false`, the reboot flag is left alone.
* `/ignition/user.json` is your Butane template rendered and translated to Ignition -- the same bytes `/ignition.json` used to return. When neither `--ignitionFile` nor a per-host `ignitionFile` is set and `config/ignition.yaml` does not exist, Booty renders an embedded minimal template (hostname only) so a fresh data directory boots without any Butane at all. A per-host `ignitionFile` that is missing is still a 500 (that is a configuration error).
* Previews (`preview=1`, no side effects): `part=user` and `part=builtin` return the children, `part=merged` returns a pretty-printed **server-side** merge (both children upconverted to spec 3.5, builtin as parent, user as child) so you can see what the node will end up with. This is display-only; the node always performs its own merge.

## Fleet status

`GET /update-check?mac=&os=&version=&image=&digest=` is what `booty-update-check` calls every 10 minutes. It answers `{"rebootRequired":bool,"running":"...","target":"...","reason":"..."}`:

* Flatcar (`os=flatcar`, from `/etc/os-release`): `rebootRequired` when the reported `VERSION_ID` differs from the Flatcar release Booty serves. Booty answers `false` while it has not downloaded a release yet.
* OSTree systems (`os=coreos`, uBlue images): `rpm-ostree status` supplies the running image reference and digest. If the host's registered `ostreeImage` is cached in Booty's registry, `rebootRequired` is set when the cached digest differs from the running one. Plain Fedora CoreOS without an image falls back to comparing `OSTREE_VERSION` with the CoreOS release Booty serves.
* Booty never says `true` when it cannot determine the target; 400/404 answers make the client leave the reboot flag alone.

Each check is recorded on the host (`running`, `lastCheck`, `rebootPending`, visible in `/booty.json`, `/hosts` and the UI), rewritten at most once a minute when nothing changed. `GET /info` gains `"fleet":{"hosts":N,"pendingReboots":N}` so you can see at a glance how many nodes are waiting on kured.

## Trust model

Booty is meant to run on a network you control. It has **no authentication**: anyone who can reach the HTTP port can register hosts, read any registered host's rendered Ignition (including a `--joinString` kubeadm token and the builtin SSH keys) via `/ignition.json?mac=` or `/ignition/user.json?mac=`, and download boot artifacts. This is inherent to PXE -- the booting machine has no credentials yet -- so treat the boot VLAN like you treat your DHCP server.

What Booty does enforce: TFTP and HTTP file serving are confined to `--dataDir` (no path traversal, no directory listings), `hardware.json`, the version pin file, temp files and the OCI blob store are never served over `/data/`, Ignition template paths from the hardware database must stay inside `--dataDir`, and all inputs (MACs, OS names, versions) are validated.

## Running as root / capabilities

Binding UDP 69 needs `CAP_NET_BIND_SERVICE`; the ARP fallback used when a client does not supply its MAC needs `CAP_NET_RAW`. The container image runs as root for that reason -- drop everything else:

```
docker run --rm --network=host \
  --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  -v $PWD/data:/data ghcr.io/jeefy/booty:main --serverIP=192.168.1.10
```

On Kubernetes set `securityContext.capabilities: {drop: [ALL], add: [NET_BIND_SERVICE, NET_RAW]}` and use `/healthz` for the liveness and readiness probes. If you use `--tftpPort` above 1024 and always pass the MAC in the URL, no capabilities are required.

## Examples

[Example ignition config / helper scripts](examples/README.md)

### Docker

```
docker run --rm -it \
  --network=host \
  --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  -v $PWD:/data/ \
  ghcr.io/jeefy/booty:main \
  --joinString="kubeadm join 192.168.1.10:6443 --token ${TOKEN} --discovery-token-ca-cert-hash sha256:${SHA_HASH}" \
  --serverIP=192.168.1.10 \
  --serverHttpPort=8080 \
  --flatcarChannel=beta \
  --coreOSChannel=testing
```

### Kubernetes

[Example deployment](examples/k8s.yaml)

This creates a configmap with the example ignition yaml config, scripts, a deployment of booty, and a service.

### PXE vs iPXE

The boot target file is different depending on whether you want to use PXE or iPXE. While iPXE is recommended due to performance, there may be some use cases where PXE is required.

To boot into PXE, use `pxelinux.cfg/default`

To boot into iPXE, use `undionly.kpxe`

## Development

```
make build      # builds the Web UI, then the Go binary with it embedded -> bin/booty
make run        # build + run against ./data with --debug
make test       # go test -race + Vitest
make lint       # golangci-lint + eslint + vue-tsc
make image      # multi-stage container build (VERSION/TIMESTAMP stamped into /info)
```

The Web UI lives in `web/` (Vue 3 + Vite). `cd web && npm run dev` starts a dev server that proxies API calls to a Booty running on `localhost:8080` (override with `VITE_API_TARGET`). See [web/README.md](web/README.md).

## Additional Thoughts

**Why?**

I like treating (most of) my machines like cattle. This is an easier and more lightweight way to tackle PXE booting and patch management.

**Can you make it do X?**

Feature requests / optimizations / PRs are welcome! Feel free to ping me [@jeefy](https://twitter.com/jeefy) on Twitter.