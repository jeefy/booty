#!/usr/bin/env bash
# Builds the iPXE binaries that Booty embeds (boot/undionly.kpxe, boot/ipxe.efi,
# boot/snponly.efi) from a pinned upstream commit inside a throwaway Debian
# container, with boot/embed.ipxe compiled in as the embedded script.
#
# Usage: hack/build-ipxe.sh            (or: make ipxe)
#   IPXE_REF=<tag|sha>  override the pinned upstream ref
#   CONTAINER_RUNTIME=  podman (default, falls back to docker)
#   CONTAINER_DNS=      extra --dns for the container (optional)
#
# Outputs are written into boot/ together with VERSION (commit, tag, build
# date, exact make invocation) and SHA256SUMS. See THIRD_PARTY_NOTICES.md.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
BOOT_DIR="$REPO_ROOT/boot"
IPXE_REPO=${IPXE_REPO:-https://github.com/ipxe/ipxe}
IPXE_REF=${IPXE_REF:-v2.0.0}
IMAGE=${IPXE_BUILD_IMAGE:-docker.io/library/debian:stable-slim}

if [[ -n "${CONTAINER_RUNTIME:-}" ]]; then
	RUNTIME=$CONTAINER_RUNTIME
elif command -v podman >/dev/null 2>&1; then
	RUNTIME=podman
elif command -v docker >/dev/null 2>&1; then
	RUNTIME=docker
else
	echo "hack/build-ipxe.sh: need podman or docker" >&2
	exit 1
fi

[[ -f "$BOOT_DIR/embed.ipxe" ]] || { echo "missing $BOOT_DIR/embed.ipxe" >&2; exit 1; }

run_args=(--rm -e IPXE_REPO="$IPXE_REPO" -e IPXE_REF="$IPXE_REF" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)")
if [[ -n "${CONTAINER_DNS:-}" ]]; then
	run_args+=(--dns "$CONTAINER_DNS")
fi
for v in http_proxy https_proxy HTTP_PROXY HTTPS_PROXY no_proxy NO_PROXY; do
	if [[ -n "${!v:-}" ]]; then
		run_args+=(-e "$v=${!v}")
	fi
done
# :Z relabels for SELinux hosts; harmless elsewhere.
run_args+=(-v "$BOOT_DIR:/out:Z")

echo "Building iPXE $IPXE_REF with $RUNTIME ($IMAGE) -> $BOOT_DIR"
exec "$RUNTIME" run "${run_args[@]}" "$IMAGE" bash -euo pipefail -c '
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
	ca-certificates git build-essential liblzma-dev mtools perl xz-utils binutils >/dev/null

if [[ "$IPXE_REF" == v* ]]; then
	git clone -q --branch "$IPXE_REF" --depth 1 "$IPXE_REPO" /ipxe
else
	git clone -q "$IPXE_REPO" /ipxe
	git -C /ipxe checkout -q "$IPXE_REF"
fi
COMMIT=$(git -C /ipxe rev-parse HEAD)
TAG=$(git -C /ipxe describe --tags --exact-match 2>/dev/null || echo none)
COMMIT_DATE=$(git -C /ipxe log -1 --format=%cI)
export SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git -C /ipxe log -1 --format=%ct)

TARGETS="bin/undionly.kpxe bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi"
MAKE_LINE="make -C src $TARGETS EMBED=/out/embed.ipxe BUILD_ID=0x${COMMIT:0:8}"
echo "+ $MAKE_LINE"
# shellcheck disable=SC2086
make -C /ipxe/src -j"$(nproc)" $TARGETS EMBED=/out/embed.ipxe BUILD_ID="0x${COMMIT:0:8}" >/tmp/ipxe-build.log 2>&1 || {
	tail -50 /tmp/ipxe-build.log
	exit 1
}

# undionly.kpxe is LZMA-compressed, so check the embedded script in the
# uncompressed link output (bin/*.tmp) and in the (uncompressed) EFI images.
for f in bin/undionly.kpxe.tmp bin-x86_64-efi/ipxe.efi bin-x86_64-efi/snponly.efi; do
	grep -q "chain tftp://\${next-server}/booty.ipxe" "/ipxe/src/$f" || { echo "embedded script missing from $f" >&2; exit 1; }
done

cp /ipxe/src/bin/undionly.kpxe /ipxe/src/bin-x86_64-efi/ipxe.efi /ipxe/src/bin-x86_64-efi/snponly.efi /out/
cd /out
{
	echo "source:      $IPXE_REPO"
	echo "commit:      $COMMIT"
	echo "tag:         $TAG"
	echo "commit-date: $COMMIT_DATE"
	echo "build-date:  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "builder:     $(. /etc/os-release; echo "$PRETTY_NAME"), $(gcc --version | head -1)"
	echo "embed:       boot/embed.ipxe (sha256 $(sha256sum embed.ipxe | cut -d" " -f1))"
	echo "make:        $MAKE_LINE"
} >VERSION
sha256sum undionly.kpxe ipxe.efi snponly.efi >SHA256SUMS
chown "$HOST_UID:$HOST_GID" undionly.kpxe ipxe.efi snponly.efi VERSION SHA256SUMS 2>/dev/null || true
ls -l undionly.kpxe ipxe.efi snponly.efi
cat VERSION SHA256SUMS
'
