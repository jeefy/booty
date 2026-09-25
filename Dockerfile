### Stage One: build the web UI
FROM node:22-alpine AS build-web
WORKDIR /app
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

### Stage Two: build the Go binary (web/dist is embedded via //go:embed)
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build-go

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

### Final stage
FROM gcr.io/distroless/static-debian12

LABEL org.opencontainers.image.source="https://github.com/jeefy/booty" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="A simple (i)PXE server for booting Flatcar, CoreOS, and Universal Blue"

COPY --from=build-go /out/booty /booty

EXPOSE 8080/tcp 69/udp

ENTRYPOINT ["/booty"]
