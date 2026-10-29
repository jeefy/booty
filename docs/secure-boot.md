# Secure Boot (UEFI HTTP Boot)

Booting machines that enforce Secure Boot: the Microsoft-signed iPXE shim, what boots under Secure Boot, CA enrolment and the signed artefact bundle.


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
| Bluefin Server | the image's own Secure Boot keys (signed netboot/disk UKIs and systemd-boot) | only once its keys are enrolled | never through iPXE: ProxyDHCP hands the firmware the signed netboot UKI directly (see [Bluefin Server](bluefin.md#bluefin-server)); a Bluefin host that reaches iPXE anyway gets the *switch to UEFI HTTP Boot* menu |
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
| `/boot/secureboot/fedora/shimx64.efi` | Fedora shim (dual-signed, Fedora vendor CA) | `shim-x64-16.1-7.x86_64.rpm` from `dl.fedoraproject.org` (Fedora 45; `updates/`, `releases/` and `development/` paths are tried) | rpm sha256 `04b7132d6316bff71427120b6aba85eb4490b2621ccb2f2559bd321ccb25f028`; other `--fedoraShimVersion`s are verified against the mirror's repository metadata (below) |
| `/boot/secureboot/fedora/grubx64.efi` | Fedora GRUB | `grub2-efi-x64-2.12-64.fc44.x86_64.rpm` (Fedora 44 updates) | rpm sha256 `3ed403514d8a8973814d553acd230a25eb355e93ebbb5a9a7da91f8393f58802` |
| `/boot/secureboot/flatcar-ca.der`, `.pem` | Flatcar Secure Boot CA | `.vendor_cert` section of `flatcar_production_image.shim` of the Flatcar release Booty serves (SHA512 from its `.DIGESTS`) | fingerprint in `/info` and `secureboot/flatcar-ca.json`; 4757.2.0: `ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2` |

The RPMs are read in Go (lead, headers, `PAYLOADCOMPRESSOR`, cpio) -- Fedora 44/45 packages are zstd-compressed; an xz payload is refused with a clear error rather than pulling in another dependency. Everything under `/boot/sb/` and `/boot/secureboot/` is `data/secureboot/current/` (plus the two CA files); paths are cleaned without redirects, there is no listing and no way out of that directory.

**Fail-closed verification.** When the checksums or digests a file is verified against cannot be fetched (after three tries), the sync refuses to install it: the Secure Boot bundle and the Flatcar CA stay where they were, exactly like a bad `--bluefinKeyring` signature. There is no fallback to an unverified download. For an unpinned Fedora RPM (a `--fedoraShimVersion`/`--fedoraGrubVersion` without a digest in code) Booty verifies the RPM's sha256 against the mirror's own repository metadata (`repomd.xml` to the primary metadata, sha256 only; xz-compressed metadata is refused); when the metadata cannot verify it, the install is refused unless `--allowUnverifiedFedoraRPMs` is set.

`GET /info` gains `"secureBoot":{"enabled","ready","bundleVersion","trusted":[...],"bootURL","flatcarCA":{"flatcarVersion","sha256","subject","notAfter","url"},"warnings":[...]}`; `ready` means the current bundle is complete on disk, `warnings` lists the Secure Boot hosts that cannot boot their OS (see [The refusal menu](#the-refusal-menu)). `/config` lists the flags. The Overview page shows the block as a *Secure Boot* card (a one-line "Secure Boot: off" when the flag is not set).

### Testing without root

```
BOOTY_PROXYDHCPPORTS=1067,5011 booty --proxyDHCP --proxyDHCPListen 127.0.0.1 --secureBoot --serverIP 127.0.0.1 ...
```

then send a captured HTTP Boot DISCOVER (`pkg/dhcp/testdata/ovmf-httpv4-discover.hex` is a real OVMF one) to UDP 1067 and decode the OFFER; `curl -I http://127.0.0.1:8080/boot/sb/ipxe-shimx64.efi` must answer `200`, `Content-Type: application/efi` and a `Content-Length`, and so must `/boot/sb//ipxe.efi`.

Out of scope for now: HTTPS Boot (some vendor firmware is built to allow only `https://`), IPv6 HTTP Boot, ARM64 (warned, the shim is not shipped), MOK enrolment, and the distro shim-to-GRUB path.
