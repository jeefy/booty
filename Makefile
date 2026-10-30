TOPDIR=$(PWD)
WHOAMI=$(shell whoami)

VERSION ?= $(shell git describe --tags --always --dirty)
TIMESTAMP ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE ?= $(WHOAMI)/booty

# Root package (embed.go) plus cmd and pkg; explicit patterns keep ./... from
# wandering into web/node_modules or other non-Go trees.
GOPKGS := . ./cmd/... ./pkg/...

web:
	cd web && npm ci && npm run build

build: web
	go build -trimpath -ldflags "-X main.version=$(VERSION) -X main.timestamp=$(TIMESTAMP)" -o bin/booty ./cmd

build-go:
	go build -trimpath -ldflags "-X main.version=$(VERSION) -X main.timestamp=$(TIMESTAMP)" -o bin/booty ./cmd

run: build
	./bin/booty --dataDir=data/ --debug

test: test-go test-web

test-go:
	go test $(GOPKGS) -race -timeout=5m

test-web:
	cd web && npm run test:unit -- --run

lint: lint-go lint-web

lint-go:
	golangci-lint run $(GOPKGS)

lint-web:
	cd web && npm run lint && npm run format:check && npm run type-check

vet:
	go vet $(GOPKGS)

fmt:
	gofmt -l -w cmd pkg embed.go

# Fails (non-zero) when any Go file is not gofmt-clean; for CI.
fmt-check:
	@test -z "$$(gofmt -l cmd pkg embed.go)" || { echo "gofmt: files need formatting:"; gofmt -l cmd pkg embed.go; exit 1; }

# Rebuilds the embedded iPXE binaries in boot/ from the pinned upstream ref
# (see hack/build-ipxe.sh and THIRD_PARTY_NOTICES.md). Run this only when
# bumping the iPXE version or changing boot/embed.ipxe, then commit boot/.
# Not part of `make build`: the binaries are checked in so `go build` needs
# no cross toolchain and works air-gapped.
ipxe:
	CONTAINER_DNS=$(CONTAINER_DNS) ./hack/build-ipxe.sh

image:
	docker build --build-arg BOOTY_VERSION=$(VERSION) --build-arg BOOTY_TIMESTAMP=$(TIMESTAMP) -t $(IMAGE) .

image-push: image
	docker push $(IMAGE)

# The image runs as nonroot (distroless :nonroot, uid 65532) with file
# capabilities on /booty (cap_net_bind_service,cap_net_raw, see Dockerfile).
# The --cap-add flags below are REQUIRED: setcap +ep makes execve fail with
# EPERM unless BOTH NET_BIND_SERVICE and NET_RAW are in the bounding set.
# --user maps the container to the host uid:gid so the bind-mounted ./data
# is writable.
image-run: image
	docker run -ti --rm \
		--user $$(id -u):$$(id -g) \
		--cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
		-v $(TOPDIR)/data:/data \
		-p 8080:8080 -p 69:69/udp \
		$(IMAGE) --debug --dataDir=/data

clean:
	rm -rf bin/ web/dist/

.PHONY: web build build-go run test test-go test-web lint lint-go lint-web vet fmt fmt-check ipxe image image-push image-run clean
