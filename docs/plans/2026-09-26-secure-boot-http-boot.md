# UEFI HTTP Boot and Secure Boot

Let UEFI machines with Secure Boot enabled boot through Booty. Today every
UEFI client gets the unsigned `ipxe.efi`, which Secure Boot firmware refuses.

## Decisions (user, 2026-09-26)

| Topic | Decision |
|---|---|
| Bootloader path | Microsoft-signed **iPXE shim** (`ipxe-shimx64.efi`, ipxe/shim `ipxe-16.1`, dual-signed 2011+2023) loading the iPXE-CA-signed `ipxe.efi`/`snponly.efi` from the ipxe release. Booty's whole iPXE menu flow stays; only the kernel-load step changes per OS. |
| Flatcar / Bluefin trust | Their kernels are signed by Flatcar's (not Microsoft-trusted) CA. Users enroll that cert in `db` on their machines; Booty ships the cert and instructions. Bluefin's installed UKI is unsigned → not bootable under Secure Boot until upstream signs (issue to be filed). |
| Verification | OVMF `secboot` with Microsoft keys enrolled in the bridged lab (FCOS end to end; Flatcar after enrolling its CA), then one real UEFI machine named by the user, which is also the first live ProxyDHCP test. |

## Verified facts (EDK2 source, UEFI 2.10, shim/GRUB/ipxe sources, PE signatures)

- **DHCP**: HTTP-Boot clients send option 60 `HTTPClient:Arch:00016:UNDI:003000` and option 93 = `0x0F` x86 / `0x10` x64 / `0x13` arm64. A proxy OFFER is accepted iff `yiaddr=0`, option 60 starts with `HTTPClient` (10 bytes compared; suffix ignored) and option 67 (or `file`) is an `http(s)://` URI (`HttpOfferTypeProxyIpUri`; paired with the real server's `DhcpOnly`/`DhcpDns` offer, selection priority P4/P5). No REQUEST to the proxy, **no port 4011**, siaddr/option 43/66 ignored. Offers are collected for the 4 s DISCOVER window. Reply ≤ 1472 bytes. Use an IP-literal URI (no DNS dependency).
- **HTTP server**: firmware does HEAD then GET (`User-Agent: UefiHttpBoot/1.0`), accepts only `200`, **never follows 3xx**, needs `Content-Type: application/efi` or a `.efi` suffix (else `EFI_NOT_FOUND`). shim and rhboot GRUB additionally require `Content-Length` and no chunked encoding. Go's `http.ServeMux` 301-cleans `//` paths → the boot handler must be raw. Some vendor firmware is built `https`-only (`PcdAllowHttpConnections=FALSE`); OVMF allows http.
- **iPXE shim**: `ipxe-shimx64.efi` derives the next loader by name substitution (`ipxe-shimx64.efi → ipxe.efi`, `snponly-shimx64.efi → snponly.efi`) in the same URL directory, no `//`. The SB iPXE build has no embedded Booty script, no `imgtrust`; it runs its default `autoboot`: DHCP (user class `iPXE`) → our ProxyDHCP answers `booty.ipxe` (already implemented) → chains `/booty.ipxe?mac=`. Downstream `kernel`/`chain` go through firmware `LoadImage`, so kernels must be trusted by `db`/`dbx` or loaded via iPXE's `shim` command with a distro shim.
- **OS signers**: FCOS live kernel = Fedora CA (`fedoraca` 2020); Fedora shim `shim-x64-16.1-7` (F45) is dual-signed and has the same vendor cert → **FCOS boots with stock keys** via `shim fedora/shimx64.efi` + `kernel`. Flatcar 4757.2.0 `flatcar_production_pxe.vmlinuz` and Bluefin's `bluefin-server-pxe-vmlinuz-26.08.0` (byte-identical to Flatcar 4593.2.5 `flatcar_production_image.vmlinuz`) are signed by `Flatcar Container Linux Secure Boot Development Signing` ← `…Development CA` (self-signed, 2024-11 → 2037-01, SHA256 `EB:B1:70:DA:…:DB:A2`, extractable from `flatcar_production_image.shim` `.vendor_cert`). Flatcar's shim is signed by that CA too, not Microsoft (shim-review never filed, flatcar/Flatcar#501). Enrolling the CA in `db` makes iPXE `kernel` of both work directly. Bluefin's installed `bluefin-server.efi` UKI and systemd-boot are unsigned.
- OVMF on this host (`edk2-ovmf-20260508`) has `UEFI HTTPv4` boot and `OVMF_CODE.secboot.fd` + `EnrollDefaultKeys.efi`.
- Microsoft UEFI CA 2011 expired 2026-06-27; new hardware may carry only the 2023 CA → prefer dual-signed shims (ipxe-16.1, Fedora F45 shim) and note it.

## Out of scope

Distro shim → GRUB path; Booty signing anything (no private keys in Booty); HTTPS boot (later: `--httpsCert`); IPv6 HTTP Boot; arm64 (log a warning as today); MOK enrolment automation; Bluefin UKI signing (upstream issue instead).

## Design

### Artefacts (`pkg/versions/secureboot.go`, synced like others into `data/secureboot/<version>/`, `current` symlink)
- From `https://github.com/ipxe/shim/releases/download/ipxe-16.1/`: `ipxe-shimx64.efi`, `snponly-shimx64.efi` (+ `.sha256` / release digest — verify; pin the expected sha256 in code for the default version like cilium-cli).
- From `https://github.com/ipxe/ipxe/releases/download/v2.0.0/ipxeboot.tar.gz`: `x86_64-sb/ipxe.efi`, `x86_64-sb/snponly.efi` (pinned sha256; the tar is fetched once and only those members extracted).
- Fedora shim+GRUB for the FCOS `shim` path: `shim-x64-16.1-7.x86_64.rpm` (F45, dual-signed) and `grub2-efi-x64-2.12-64.fc44.x86_64.rpm` → extract `EFI/fedora/shimx64.efi` (+ `grubx64.efi` — needed because shim verifies and *then* launches the image iPXE hands it; iPXE's `shim` command needs only `shimx64.efi`; confirm in QEMU whether `grubx64.efi` is required at all and drop it if not). RPM = cpio in a lead+header envelope: implement a minimal rpm→cpio reader in Go (payload compression is zstd for F44/F45 — check the header tag `PAYLOADCOMPRESSOR`; `github.com/klauspost/compress/zstd` is already a dependency? verify; else skip RPMs and take the files from the FCOS live ISO's `images/efiboot.img` (FAT) — also implementable in Go with a small FAT reader; pick whichever needs no new deps; if both need one, `klauspost/compress` is acceptable).
- Flatcar CA: extracted at sync time from `flatcar_production_image.shim` (`.vendor_cert` section parse) of the Flatcar release Booty already tracks, saved as `data/secureboot/flatcar-ca.der`/`.pem`; served at `/boot/secureboot/flatcar-ca.der` and shown in `/info` + UI with its SHA256 so users can enroll it (`sbctl enroll-keys` custom `db` / firmware setup UI). Also the Bluefin note.
- Flags: `--secureBootIPXEVersion` (`ipxe-16.1`/`v2.0.0` pins), `--fedoraShimVersion`, `--secureBootTrusted` comma list of CAs the fleet firmware trusts: `microsoft` (implied), `flatcar` (user asserts they enrolled it). `/info` gains `secureBoot: {version, flatcarCA sha256, trusted}`.

### DHCP (`pkg/dhcp/pxe.go`)
- Classify a DISCOVER as HTTP Boot when option 93 ∈ {0x0F, 0x10, 0x13} or option 60 starts with `HTTPClient`.
- OFFER: `yiaddr=0`, option 53 OFFER, 54 server id, **60 `HTTPClient`**, **67 `http://<serverIP>:<serverHttpPort>/boot/ipxe-shimx64.efi`** (`snponly-shimx64.efi` when `--efiBootloader=snponly`), no option 43, no 4011 follow-up (ignore REQUESTs from HTTP clients). arm64 → `ipxe-shimaa64.efi` only if we ship it (we don't; warn as today). `:80` omitted from the URL like elsewhere.
- Keep PXE (option 60 `PXEClient`) behaviour byte-identical (golden test on the existing OFFER/ACK fixtures).
- iPXE user-class re-DHCP from the SB iPXE: already returns `booty.ipxe` (bare name resolved against siaddr via TFTP). The SB iPXE build has TFTP; confirm in QEMU. If TFTP is unavailable on the real box (some firmware/iPXE combos), set option 67 for user-class `iPXE` to the full `http://…/booty.ipxe` URL instead (iPXE accepts URLs in filename) — decide in QEMU and make it the default if it works everywhere.

### HTTP (`pkg/server/http.go`)
- `/boot/` handler becomes raw (registered as a prefix on the mux but does its own path handling: collapse `//`, no redirects), supports HEAD, sets `Content-Length` (files are served from disk or embed → known sizes), `Content-Type: application/efi` for `.efi`, `application/octet-stream` otherwise, `Cache-Control: no-cache`. Serves embedded iPXE binaries (existing) plus `data/secureboot/current/*`.
- `/boot/ipxe.efi` (unsigned, embedded) stays for non-SB clients.

### iPXE script (`pkg/tftp/pxe_config.go` / bluefin.ipxe)
- Detect Secure Boot at script time: iPXE exposes `${platform}` (efi) but not SB state; the ProxyDHCP path knows the client came via HTTP Boot → record `httpBoot=true` on the host (like `booted`) when the HTTP-Boot OFFER is sent, and pass `&sb=1`? Simpler and robust: the SB iPXE binary is only ever reached via the HTTP-Boot OFFER, so Booty marks the MAC `secureBoot: true` at OFFER time (persisted, cleared when a PXE OFFER is sent instead). The script for such a host:
  - coreos: `shim ${base}/boot/secureboot/fedora/shimx64.efi` then `kernel …` / `initrd …` / `boot` (iPXE ≥1.21 `shim` command; verify it exists in the SB build's feature list — `ipxe.org/secboot` says it does).
  - flatcar / bluefin: if `flatcar` ∈ `--secureBootTrusted` → plain `kernel` (firmware verifies against the enrolled CA); else render a menu that explains ("Flatcar's Secure Boot CA is not trusted by this firmware; enroll `/boot/secureboot/flatcar-ca.der` or disable Secure Boot") with *Boot from disk* / *Reboot* only, and add a `/cluster`+`/info` warning. Bluefin additionally warns that the installed system will not boot under Secure Boot (unsigned UKI) — Booty refuses `doInstall` for a `secureBoot` bluefin host with a clear message.
- Host record + UI: `secureBoot` badge in the hosts list; `/info` secureBoot block on Home.

## Slices / PRs
1. **PR S1 — protocol + artefacts + serving**: DHCP HTTP-Boot OFFER, raw `/boot/` handler, `pkg/versions/secureboot.go` (ipxe shim + SB iPXE + Fedora shim/grub + Flatcar CA extraction), flags, `/info`, README "Secure Boot" section, tests (DHCP fixtures incl. a captured OVMF HTTPv4 DISCOVER, handler HEAD/`//`/Content-Type tests, PE `.vendor_cert` parser test against the real Flatcar shim bytes checked in as a fixture — it's 950 KB; instead check in only the `.vendor_cert` section bytes).
2. **PR S2 — Secure-Boot-aware scripts + UI**: `secureBoot` host flag set from the OFFER, per-OS script variants, refusal/warnings, UI badge + Home block, docs for enrolling the Flatcar CA (`sbctl`, firmware menu), Bluefin caveat.
3. **QEMU (lab)**: OVMF `OVMF_CODE.secboot.fd` + vars with Microsoft keys (`EnrollDefaultKeys.efi` or the shipped `OVMF_VARS.secboot.fd` — check whether it already has MS PK/KEK/db), `-machine q35,smm=on`, bridged net: (a) FCOS host → firmware `UEFI HTTPv4` → ipxe-shim → ipxe → booty.ipxe → `shim` + kernel → FCOS up, `mokutil --sb-state` = enabled inside the guest; (b) Flatcar host → refusal menu; enroll Flatcar CA in the vars (`sbctl`/`EnrollDefaultKeys`-style or `efi-updatevar` from the guest… simplest: a one-off OVMF vars image with the CA appended via `virt-fw-vars --add-db`) → Flatcar boots; (c) legacy PXE UEFI (non-SB) and BIOS clients unchanged; (d) reboot idempotence of the `secureBoot` flag. Evidence appended to this plan.
4. **Real hardware**: user names one UEFI box; Booty homelab gets `--proxyDHCP` on (first live ProxyDHCP run — coordinate with UniFi DHCP still pointing at `undionly.kpxe` for the BIOS fleet; ProxyDHCP and the UniFi options coexist, both point at Booty).

## Acceptance
- Unit: DHCP classification table (arch 0x10 / `HTTPClient` / `PXEClient` / user-class `iPXE`), OFFER bytes contain opt 60 `HTTPClient` + opt 67 URL and no opt 43; PXE fixtures byte-identical; handler: `HEAD /boot/ipxe-shimx64.efi` → 200 + `Content-Length` + `application/efi`; `GET /boot//ipxe.efi` → 200 (no 301); `.vendor_cert` parse of the Flatcar shim section == known SHA256 `EBB170DA…DBA2`.
- Lint/tests/web green; README `--help` block regenerated.
- QEMU evidence as above; `mokutil --sb-state` "SecureBoot enabled" on the booted FCOS and Flatcar guests.
- Homelab roll after each PR (no behaviour change for the BIOS fleet; `--proxyDHCP` stays off until step 4).

## Risks
- Vendor firmware that only allows HTTPS Boot — documented; `--httpsCert` later.
- Firmware that ignores proxy HTTP-Boot offers (some Dell/HP?) — real-hardware step will tell; fallback is putting `HTTPClient`+URL on the real DHCP server, which UniFi can't do.
- Flatcar rotates its "temporary" CA (did in 2026-04) → users must re-enroll; Booty shows the current fingerprint and warns when the synced shim's CA changes.
- ipxe SB build lacks Booty's embedded script: relies on ProxyDHCP answering the iPXE user-class DHCP, which needs `--proxyDHCP` on. Documented as a hard requirement for Secure Boot.

## Evidence: S1 QEMU runs (Sisyphus, 2026-09-26, bridged lab, Booty owns ProxyDHCP 67/4011 + TFTP 69, dnsmasq leases only)

Firmware: `OVMF_CODE.secboot.fd` (`-machine q35,smm=on`, pflash secure) with the shipped `OVMF_VARS.secboot.fd` (Microsoft PK/KEK/db pre-enrolled).

- **Baseline**: UEFI PXE → unsigned `ipxe.efi` → firmware `Access Denied` / `Secure Boot` violation → falls through to `>>Start HTTP Boot over IPv4` (nobody answered before S1).
- **First live ProxyDHCP run found a real bug**: the client's broadcast DHCPREQUEST (port 67) for the *DHCP server's* lease was ACKed by Booty (yiaddr 0 + boot file) → EDK2 `Lease confirmed isn't the same as that in the offer` → DHCPDECLINE → PXE never reached TFTP. Fixed: REQUESTs answered on 4011 only. After the fix, non-SB UEFI PXE via ProxyDHCP works (OFFER → 4011 ACK `ipxe.efi` → TFTP → iPXE → 4011 ACK `booty.ipxe` → menu).
- **HTTP Boot**: OVMF sent 4 HTTPv4 DISCOVERs (0/4/12/28 s) with only dnsmasq's address offer and gave up — confirms the proxy URI OFFER is required. With Booty's OFFER (opt 60 `HTTPClient`, opt 67 `http://10.77.0.1:18094/boot/sb/ipxe-shimx64.efi`): `HEAD`+`GET ipxe-shimx64.efi`, shim probed `revocations_sku.efi`/`revocations_sbat.efi`/`shim_certificate_0.efi` (404, fine), `GET ipxe.efi` (SB build), then **iPXE fetched `/boot/sb/autoexec.ipxe`** — and did *not* use the ProxyDHCP boot file. Serving Booty's chain script (with `sb=1`) as `autoexec.ipxe` completes the path with no TFTP involved.
- **FCOS (`sb-fcos`, stock Microsoft keys)**: `shim …/boot/secureboot/fedora/shimx64.efi` + `kernel` → live kernel accepted → Ignition (`/ignition.json`, builtin, user) applied → SSH: `SecureBoot enabled`, hostname `sb-fcos`, `/sys/firmware/efi` present. Without the `shim` line the same kernel failed with iPXE `Error 0x7f04819a` (image verification).
- **Flatcar (`sb-flatcar`)**: stock keys → `0x7f04819a` as expected. After `virt-fw-vars --add-db` of Booty's extracted `flatcar-ca.der` (CN=Flatcar Container Linux Secure Boot Development CA, `EB:B1:70:DA…DB:A2`) into the vars: boots, `SecureBoot-…` efivar = 1, Flatcar 4757.2.0, Ignition applied.
