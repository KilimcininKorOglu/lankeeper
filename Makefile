BINARY  := home-router
DIST_DIR := dist
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)
AMD64_BINARY := $(DIST_DIR)/$(BINARY)-linux-amd64
ARM64_BINARY := $(DIST_DIR)/$(BINARY)-linux-arm64
AMD64_ISO := $(DIST_DIR)/$(BINARY)-installer-amd64.iso
ARM64_ISO := $(DIST_DIR)/$(BINARY)-installer-arm64.iso
DEBIAN_AMD64_ISO ?= $(DEBIAN_ISO)
DEBIAN_ARM64_ISO ?=
DOCKER ?= docker
ISO_BUILDER_AMD64 ?= home-router-iso-builder-amd64
ISO_BUILDER_ARM64 ?= home-router-iso-builder-arm64

.PHONY: build test lint clean dev cross cross-amd64 cross-arm64 cross-all install iso iso-amd64 iso-arm64 iso-all docker-builder-amd64 docker-builder-arm64 docker-builders release check

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/home-router

dev:
	go build -o $(BINARY) ./cmd/home-router

test:
	go test ./... -race -count=1

lint:
	golangci-lint run

clean:
	rm -f $(BINARY)

cross: cross-amd64
	cp $(AMD64_BINARY) $(BINARY)

cross-amd64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(AMD64_BINARY) ./cmd/home-router

cross-arm64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(ARM64_BINARY) ./cmd/home-router

cross-all: cross-amd64 cross-arm64

install: cross
	sudo bash deploy/install.sh ./$(BINARY)

docker-builder-amd64:
	$(DOCKER) build --platform linux/amd64 -t $(ISO_BUILDER_AMD64) -f deploy/iso/Dockerfile.build .

docker-builder-arm64:
	$(DOCKER) build --platform linux/arm64 -t $(ISO_BUILDER_ARM64) -f deploy/iso/Dockerfile.build .

docker-builders: docker-builder-amd64 docker-builder-arm64

iso: iso-amd64

iso-amd64: cross-amd64 docker-builder-amd64
	@test -n "$(DEBIAN_AMD64_ISO)" || (echo "DEBIAN_AMD64_ISO or DEBIAN_ISO is required" >&2; exit 1)
	$(DOCKER) run --platform linux/amd64 --rm \
		-v $(CURDIR):/build \
		-v $(DEBIAN_AMD64_ISO):/debian.iso:ro \
		$(ISO_BUILDER_AMD64) /debian.iso /build/$(AMD64_BINARY) amd64 /build/$(AMD64_ISO)

iso-arm64: cross-arm64 docker-builder-arm64
	@test -n "$(DEBIAN_ARM64_ISO)" || (echo "DEBIAN_ARM64_ISO is required" >&2; exit 1)
	$(DOCKER) run --platform linux/arm64 --rm \
		-v $(CURDIR):/build \
		-v $(DEBIAN_ARM64_ISO):/debian.iso:ro \
		$(ISO_BUILDER_ARM64) /debian.iso /build/$(ARM64_BINARY) arm64 /build/$(ARM64_ISO)

iso-all: iso-amd64 iso-arm64

release: cross-amd64
	mkdir -p dist
	tar czf dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz $(AMD64_BINARY) deploy/ configs/ web/locales/
	@echo "Release archive: dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz"

check:
	sudo bash deploy/install.sh --check
