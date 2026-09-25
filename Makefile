TOPDIR=$(PWD)
WHOAMI=$(shell whoami)

VERSION ?= $(shell git describe --tags --always --dirty)
TIMESTAMP ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE ?= $(WHOAMI)/booty

web:
	cd web && npm ci && npm run build

build: web
	go build -trimpath -ldflags "-X main.version=$(VERSION) -X main.timestamp=$(TIMESTAMP)" -o bin/booty ./cmd

build-go:
	go build -trimpath -ldflags "-X main.version=$(VERSION) -X main.timestamp=$(TIMESTAMP)" -o bin/booty ./cmd

run: build
	./bin/booty --dataDir=data/ --debug

test:
	go test ./... -race
	cd web && npm run test:unit -- --run

lint:
	golangci-lint run ./...
	cd web && npm run lint && npm run type-check

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd pkg embed.go

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

# Runs as root because distroless nonroot cannot receive ambient capabilities;
# drop everything and re-add only the caps booty needs (TFTP :69, arping).
image-run: image
	docker run -ti --rm \
		--cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
		-v $(TOPDIR)/data:/data \
		-p 8080:8080 -p 69:69/udp \
		$(IMAGE) --debug --dataDir=/data

clean:
	rm -rf bin/ web/dist/

.PHONY: web build build-go run test lint vet fmt ipxe image image-push image-run clean
