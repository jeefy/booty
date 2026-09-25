# Third-party notices

Booty itself is MIT licensed (see [LICENSE](LICENSE)). The binary additionally
embeds the following third-party software.

## iPXE

The files under [`boot/`](boot/) -- `undionly.kpxe` (BIOS), `ipxe.efi` and
`snponly.efi` (x86-64 UEFI) -- are unmodified builds of **iPXE**, embedded in
the Booty binary via `//go:embed` and served over TFTP and `GET /boot/<name>`.

* Upstream: <https://github.com/ipxe/ipxe> (<https://ipxe.org>)
* License: GNU General Public License v2.0 with the
  [Unmodified Binary Distribution Licence (UBDL)](https://ipxe.org/licensing)
  exception; see `COPYING`, `COPYING.GPLv2` and `COPYING.UBDL` in the upstream
  tree at the pinned commit.
* Pinned source: tag `v2.0.0`, commit `12798ec29aa8a64d8675c4378b99f5fe28447afb`.
  [`boot/VERSION`](boot/VERSION) records the commit, tag, build date and the
  exact `make` invocation; [`boot/SHA256SUMS`](boot/SHA256SUMS) lists the
  digests of the committed binaries (`sha256sum -c` from inside `boot/`).
* Embedded script: the binaries are built with
  [`boot/embed.ipxe`](boot/embed.ipxe) compiled in (`EMBED=`). That script is
  part of Booty and is MIT licensed; it is the only thing added to the
  upstream build, no iPXE source file is patched.

### Corresponding source

The complete corresponding source for the binaries in `boot/` is the upstream
iPXE repository at the pinned commit above, plus `boot/embed.ipxe` from this
repository. No other modifications are applied.

### Rebuilding

```
make ipxe            # podman (falls back to docker), writes boot/*
CONTAINER_DNS=192.168.50.17 make ipxe   # if the container needs an explicit DNS server
IPXE_REF=<tag|sha> ./hack/build-ipxe.sh # build a different upstream ref
```

[`hack/build-ipxe.sh`](hack/build-ipxe.sh) runs a throwaway
`debian:stable-slim` container, installs the iPXE build dependencies, clones
upstream at the pinned ref and runs

```
make -C src bin/undionly.kpxe bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi \
  EMBED=<repo>/boot/embed.ipxe BUILD_ID=0x<first 8 hex digits of the commit>
```

`BUILD_ID` is pinned (iPXE otherwise randomises it) and `SOURCE_DATE_EPOCH` is
set to the commit date, so a rebuild of the same ref yields byte-identical
binaries and the checked-in `SHA256SUMS` can be re-verified independently.
Run it only when bumping the iPXE version or changing `boot/embed.ipxe`, then
commit `boot/` (binaries, `VERSION`, `SHA256SUMS`) and update the commit in
this file.
