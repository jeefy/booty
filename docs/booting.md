# Booting: DHCP, iPXE and ProxyDHCP

How hosts reach Booty: classic DHCP with the embedded iPXE bootloaders, the boot sequence, auto-registration, and the optional zero-touch ProxyDHCP server.

## Booting: DHCP and the iPXE bootloaders

Booty ships three iPXE bootloaders, built from a pinned upstream release (see [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) and [`boot/VERSION`](../boot/VERSION)) and embedded in the binary. They are served at the root of the TFTP namespace and over HTTP as `GET /boot/<name>`, and are also copied into `--dataDir` on start-up if missing (so `/data/<name>` keeps working for anyone who pointed DHCP there):

| DHCP client architecture (option 93) | Firmware | `filename` |
|---|---|---|
| `00:00` | BIOS / legacy PC | `undionly.kpxe` |
| `00:07`, `00:09` | x86-64 UEFI | `ipxe.efi` (or `snponly.efi`) |

`ipxe.efi` carries iPXE's own network drivers and works on most hardware; `snponly.efi` only talks to the firmware's UEFI network stack (SNP), so prefer it on machines whose NIC misbehaves with `ipxe.efi` (no link, hangs after "Initialising devices", or firmware that refuses to hand over the NIC) -- it is smaller and boots through whatever driver the firmware already brought up. Legacy pxelinux/syslinux booting is not supported; only iPXE.

All three carry the same embedded script ([`boot/embed.ipxe`](../boot/embed.ipxe)): it runs DHCP (retrying every 5 s until it gets a lease), then chains `tftp://${next-server}/booty.ipxe`, falls back to `http://${next-server}/booty.ipxe?mac=${mac}` and, if neither is reachable, prints a message and drops to the iPXE shell. `${next-server}` is the DHCP `next-server`/`siaddr`, i.e. Booty, so no `user-class = "iPXE"` chainload-loop trickery is needed in the DHCP config: the bootloader never asks for the DHCP `filename` again.

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
4. The OS fetches `http://<serverIP>/ignition.json?mac=<mac>`. For a registered host that is a tiny wrapper whose `ignition.config.merge` points at two children: Booty's builtin fragment (`/ignition/builtin.json`) and the host's Butane template rendered to Ignition (`/ignition/user.json`; variables: `.Hostname`, `.ServerIP`, `.JoinString`, `.OSTreeImage`). Ignition fetches and merges them itself, later entries winning, so your template overrides the builtin (and the `--profile` appended to it). The wrapper fetch records `booted`/`ip` for the host and, by default, clears a pending `doInstall`; the child fetches have no side effects. With `--doInstallClearOn=booted` the flag instead stays set until the installed system calls `POST http://<serverIP>/booted?mac=<mac>` (the builtin `booty-booted.service` does exactly that), so a failed install keeps the host in install mode. `--doInstallClearOn=next-boot` is meant for Bluefin Server; see [Installing to disk](bluefin.md#installing-to-disk). Add `&preview=1` (the UI does) to look at a config without recording a boot. Unregistered hosts receive an Ignition config whose only unit reboots the machine (the "brig").
5. Kernel/initrd/rootfs are served from `/data/`. Every OS keeps its releases in `data/<os>/<version>/` (`flatcar/4757.2.0/`, `coreos/44.20260913.2.1/`, `bluefin/26.09.673/`) behind relative `current`, `previous` and `lastGood` symlinks, and the boot scripts name the release directory the host boots (its `targetVersion`, else `current`), so the kernel and initrd always come from the same release and updates are atomic. The pre-release-directory names (`data/flatcar_production_pxe.vmlinuz`, `data/fedora-coreos-<version>-live-*`) stay as symlinks into `<os>/current` for anything that still uses them; see [Releases and retention](storage.md#releases-and-retention-p1).

Bluefin Server hosts boot a signed UKI instead and fetch their own Ignition config from `/bluefin/<mac>/`: straight from the firmware over UEFI HTTP Boot, or, with Secure Boot off, chainloaded by `/booty.ipxe` in step 3; on legacy BIOS step 3 boots the UKI's kernel and initrd instead (diskless only). See [How a Bluefin host boots](bluefin.md#how-a-bluefin-host-boots), [Chainloading from iPXE](bluefin.md#chainloading-from-ipxe-secure-boot-off) and [Legacy BIOS diskless boot](bluefin.md#legacy-bios-diskless-boot).

`/booty.ipxe` and `/ignition.json` fall back to an ARP lookup of the requesting IP when called without `?mac=`; the shipped scripts always pass it. The lookup times out after 500 ms so a request from an unreachable or quiet address is not held up, and an IP whose lookup failed is not retried for 30 s.

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

**Trust model caveat.** Auto-registration turns "any device on the boot VLAN can PXE boot" into "any device on the boot VLAN becomes a registered host that receives your Ignition config" (including `--joinString` and the builtin SSH keys, which unregistered hosts can already read; see [Trust model](trust-model.md#trust-model)). For a kubeadm cluster this also means a stray or wrongly named machine joins as a Node; a hostname collision creates a duplicate Node object or hijacks an existing one. The brig is the safer default; turn auto-registration on when the boot network is closed and you want zero-touch provisioning. Auto-registered hosts start with `doInstall` unset, so for CoreOS and Bluefin installs you still flip it in the UI.

### The hardware database on disk

`hardware.json` writes are batched for routine bookkeeping only: changes from boot bookkeeping, health reports and the power tracker are flushed at most every 2 s, and on shutdown. Registrations and unregistrations (`/register`, `/unregister`, the UI) and every one-shot transition (`doInstall`, `mode`, `installServedAt`, `targetVersion`, the autopilot pin) write synchronously, together with any pending batched change. If you edit `hardware.json` by hand while Booty is running your edit wins: Booty notices the file changed on disk, drops its own unflushed batched changes with a warning in the log, and reloads.

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
| option 60 `HTTPClient` (UEFI HTTP Boot), registered Bluefin host | its netboot UKI URL, or nothing once installed; see [Bluefin Server](bluefin.md#bluefin-server) |
| option 60 `HTTPClient` (UEFI HTTP Boot), any other host | ignored unless `--secureBoot` is on, see [Secure Boot](secure-boot.md#secure-boot-uefi-http-boot) |

`--efiBootloader=snponly` swaps `ipxe.efi` for `snponly.efi` in the x86-64 UEFI rows (and the matching shim for HTTP Boot clients); the default `ipxe` keeps today's files.

The last row is iPXE's own second-stage DHCP: once `undionly.kpxe`/`ipxe.efi` is running it repeats DHCP with user class `iPXE`. Handing it another iPXE binary would loop, so Booty gives it the `booty.ipxe` stub instead. iPXE resolves a bare filename against `tftp://${next-server}/`, and `${next-server}` falls back to the `siaddr` of the ProxyDHCP reply when the real DHCP server's `siaddr` is empty (iPXE keeps ProxyDHCP settings in a lower-priority `proxydhcp` settings block that is consulted whenever the primary DHCP settings lack a value), so this resolves to Booty. If your iPXE build has an embedded script the filename is ignored and the embedded script runs instead.

Ports 67 and 4011 are privileged, so the container needs `--network=host`/`hostNetwork: true` and `CAP_NET_BIND_SERVICE` (already granted in the examples above). `--proxyDHCPListen` takes an IPv4 address or an interface name; prefer the interface name on multi-homed hosts, because a socket bound to a unicast address does not receive the broadcast DHCPDISCOVERs on Linux. Only iPXE's `ipxe.efi` needs to be present in `--dataDir` for UEFI clients; `undionly.kpxe` and `booty.ipxe` are built in. For tests without root the two ports can be overridden with the environment variable `BOOTY_PROXYDHCPPORTS=1067,5011` (test-only, not a flag).
