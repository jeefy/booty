# Booty

A simple (i)PXE Server for booting Flatcar-Linux, CoreOS, and [Universal Blue](https://universal-blue.org)

```
> booty --help

Easy iPXE server for Flatcar, CoreOS, and more

Usage:
  booty [flags]

Flags:
      --coreOSArchitecture string    Architecture to use for CoreOS downloads (default "x86_64")
      --coreOSChannel string         CoreOS channel to look for updates (default "stable")
      --dataDir string               Directory to store stateful data (default "/data")
      --debug                        Enable debug logging
      --flatcarArchitecture string   Architecture to use for the Flatcar downloads (default "amd64")
      --flatcarChannel string        Flatcar channel to look for updates (default "stable")
      --flatcarVersion string        Pin a specific Flatcar version (e.g. 3815.2.0). When empty, tracks the latest version on the configured channel
  -h, --help                         help for booty
      --httpPort int                 Port to use for the HTTP server (default 8080)
      --joinString string            The kubeadm join string to use to auto-join to a K8s cluster (kubeadm join 192.168.1.10:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:SHA_HASH)
      --serverHttpPort int           Alternative HTTP port to use for clients (default 80)
      --serverIP string              IP address that clients can connect to (default "127.0.0.1")
      --tftpBlockSize int            TFTP block size to negotiate with clients (default 1468)
      --tftpPort int                 UDP port to use for the TFTP server (default 69)
      --updateSchedule string        Cron schedule for the Flatcar/CoreOS version checks and OSTree image sync (default "*/5 * * * *")
      --webDir string                Directory with the built Web UI, used when no UI is embedded in the binary (default "./web/dist")
```

Every flag can also be set through the environment as `BOOTY_<FLAGNAME>` (upper-cased, e.g. `BOOTY_SERVERIP=192.168.1.10`, `BOOTY_JOINSTRING=...`). The older `IGNITION_FILE`, `HARDWARE_MAP`, `FLATCAR_VERSION_PIN`, `DEPS_PXELINUX_URL` and `DEPS_LDLINUX_URL` names still work.

## Features

* (i)PXE boot into the latest Flatcar-Linux or CoreOS
* MAC address based hostnames
* Automatic conversion of Butane YAML to Ignition JSON
  * Variable injection in Butane/Ignition
* JSON "Hardware Database" (Containing boot-time config data)
* Automatic updates retrieved from Flatcar-Linux and CoreOS
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))
* Web UI to add/edit/remove hosts
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
4. The OS fetches `http://<serverIP>/ignition.json?mac=<mac>`; Booty renders the host's Butane template (variables: `.Hostname`, `.ServerIP`, `.JoinString`, `.OSTreeImage`) and translates it to Ignition, records `booted`/`ip` for the host and clears a pending `doInstall`. Add `&preview=1` (the UI does) to look at a rendered config without recording a boot. Unregistered hosts receive an Ignition config whose only unit reboots the machine (the "brig").
5. Kernel/initrd/rootfs are served from `/data/`. Flatcar artifacts live in `data/flatcar/<version>/` behind symlinks at the old paths, so the kernel and initrd always come from the same release and updates are atomic.

Legacy PXE clients ask for `pxelinux.cfg/01-<mac>` before `pxelinux.cfg/default`; Booty uses that MAC the same way.

## Trust model

Booty is meant to run on a network you control. It has **no authentication**: anyone who can reach the HTTP port can register hosts, read any registered host's rendered Ignition (including a `--joinString` kubeadm token) via `/ignition.json?mac=`, and download boot artifacts. This is inherent to PXE -- the booting machine has no credentials yet -- so treat the boot VLAN like you treat your DHCP server.

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