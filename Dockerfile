### Stage One: build the web UI
# Pinned by digest (docker buildx imagetools inspect node:26-alpine). The tag
# is kept for readability; bump by resolving the new tag's digest. The major
# must match web/.nvmrc.
FROM --platform=$BUILDPLATFORM docker.io/library/node:26-alpine@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94 AS build-web
WORKDIR /app
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

### Stage Two: build the Go binary (web/dist is embedded via //go:embed)
# Pinned to go1.26.10 to match the toolchain directive in go.mod (earlier
# 1.26 patch releases carry stdlib vulnerabilities reported by govulncheck).
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26.10-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS build-go

ARG TARGETARCH
ARG BOOTY_VERSION=dev
ARG BOOTY_TIMESTAMP=

WORKDIR /app

# Layer-cache Go module downloads
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Place the built web UI where //go:embed all:web/dist expects it
COPY --from=build-web /app/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${BOOTY_VERSION} -X main.timestamp=${BOOTY_TIMESTAMP}" \
    -o /out/booty ./cmd

RUN mkdir -p /out/data

# File capabilities let the binary bind :67 (DHCP), :69 (TFTP) and :80
# (HTTP when --httpPort=80) and use raw ARP sockets while running as
# nonroot (ProxyDHCP's :4011 is above 1023 and needs no capability):
# exec() raises the file's permitted+effective caps for any UID, as long
# as the container's bounding set includes them
# (--cap-add NET_BIND_SERVICE,NET_RAW / the k8s securityContext; ambient
# caps are not needed). BuildKit's COPY preserves the xattr into the
# final stage.
RUN apk add --no-cache libcap \
    && setcap 'cap_net_bind_service,cap_net_raw=+ep' /out/booty

### Final stage
# :nonroot runs as uid 65532 (nonroot, not nobody/65534); combined with
# the file capabilities above the server needs no root. Pinned by digest
# (gcr.io/distroless).
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

LABEL org.opencontainers.image.source="https://github.com/jeefy/booty" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="A simple (i)PXE server for booting Flatcar, CoreOS, and Universal Blue"

COPY --from=build-go /out/booty /booty
COPY --from=build-go --chown=65532:65532 /out/data /data

EXPOSE 8080/tcp 69/udp

# No HEALTHCHECK: the distroless static:nonroot base has no shell, curl or
# wget, and adding a helper binary for the check alone is not worth the
# surface. The server exposes /healthz; wire liveness/readiness probes to it
# in the orchestrator (see examples/k8s.yaml), or use `docker inspect` health
# from the outside.

USER 65532:65532

ENTRYPOINT ["/booty"]
