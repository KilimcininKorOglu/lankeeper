BINARY  := lankeeper
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
AMD64_RELEASE_DIR := $(DIST_DIR)/release-amd64
ARM64_RELEASE_DIR := $(DIST_DIR)/release-arm64
DEBIAN_AMD64_ISO ?= source_iso/debian-12.10.0-amd64-netinst.iso
DEBIAN_ARM64_ISO ?= source_iso/debian-12.10.0-arm64-netinst.iso
DOCKER ?= docker
ISO_BUILDER_AMD64 ?= lankeeper-iso-builder-amd64
ISO_BUILDER_ARM64 ?= lankeeper-iso-builder-arm64

.PHONY: build test lint clean dev cross cross-amd64 cross-arm64 cross-all install iso iso-amd64 iso-arm64 iso-all docker-builder-amd64 docker-builder-arm64 docker-builders release release-archives release-amd64 release-arm64 release-all checksums check

build:
	mkdir -p $(DIST_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY) ./cmd/lankeeper

dev:
	mkdir -p $(DIST_DIR)
	go build -o $(DIST_DIR)/$(BINARY) ./cmd/lankeeper

test:
	go test ./... -race -count=1

lint:
	golangci-lint run

clean:
	rm -rf $(DIST_DIR)

cross: cross-amd64

cross-amd64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(AMD64_BINARY) ./cmd/lankeeper

cross-arm64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(ARM64_BINARY) ./cmd/lankeeper

cross-all: cross-amd64 cross-arm64

install: cross
	sudo bash deploy/install.sh $(DIST_DIR)/$(BINARY)

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
		$(ISO_BUILDER_AMD64) /debian.iso /build/$(AMD64_BINARY) amd64 /build/$(AMD64_ISO) $(VERSION)

iso-arm64: cross-arm64 docker-builder-arm64
	@test -n "$(DEBIAN_ARM64_ISO)" || (echo "DEBIAN_ARM64_ISO is required" >&2; exit 1)
	$(DOCKER) run --platform linux/arm64 --rm \
		-v $(CURDIR):/build \
		-v $(DEBIAN_ARM64_ISO):/debian.iso:ro \
		$(ISO_BUILDER_ARM64) /debian.iso /build/$(ARM64_BINARY) arm64 /build/$(ARM64_ISO) $(VERSION)

iso-all: iso-amd64 iso-arm64

release: release-archives

release-archives: release-amd64 release-arm64
	$(MAKE) checksums VERSION=$(VERSION)

release-amd64: cross-amd64
	mkdir -p dist
	mkdir -p $(AMD64_RELEASE_DIR)
	cp $(AMD64_BINARY) $(AMD64_RELEASE_DIR)/$(BINARY)
	tar czf dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz -C $(AMD64_RELEASE_DIR) $(BINARY)
	@echo "Release archive: dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz"

release-arm64: cross-arm64
	mkdir -p dist
	mkdir -p $(ARM64_RELEASE_DIR)
	cp $(ARM64_BINARY) $(ARM64_RELEASE_DIR)/$(BINARY)
	tar czf dist/$(BINARY)-$(VERSION)-linux-arm64.tar.gz -C $(ARM64_RELEASE_DIR) $(BINARY)
	@echo "Release archive: dist/$(BINARY)-$(VERSION)-linux-arm64.tar.gz"

release-all: release-amd64 release-arm64 iso-all
	$(MAKE) checksums VERSION=$(VERSION)

checksums:
	@cd dist && { for f in $(BINARY)-$(VERSION)-linux-*.tar.gz $(BINARY)-installer-*.iso; do [ -f "$$f" ] && shasum -a 256 "$$f"; done; } > SHA256SUMS
	@echo "Checksums: dist/SHA256SUMS"

check:
	sudo bash deploy/install.sh --check
