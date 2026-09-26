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
AMD64_ISO := $(DIST_DIR)/$(BINARY)-$(VERSION)-installer-amd64.iso
ARM64_ISO := $(DIST_DIR)/$(BINARY)-$(VERSION)-installer-arm64.iso
AMD64_RELEASE_DIR := $(DIST_DIR)/release-amd64
ARM64_RELEASE_DIR := $(DIST_DIR)/release-arm64
# A missing source image is fetched from the Debian archive. The point
# release has to be one deploy/iso/debian-images.sha512 lists, because
# build-iso.sh checks the image against those signed digests before any
# parser reads it: the download is a convenience, never a trust decision.
DEBIAN_RELEASE := 12.10.0
DEBIAN_CDIMAGE := https://cdimage.debian.org/cdimage/archive/$(DEBIAN_RELEASE)
DEBIAN_AMD64_ISO ?= source_iso/debian-$(DEBIAN_RELEASE)-amd64-netinst.iso
DEBIAN_ARM64_ISO ?= source_iso/debian-$(DEBIAN_RELEASE)-arm64-netinst.iso
# install builds and installs on the machine it runs on, so the binary
# has to match that machine rather than a fixed target.
HOST_ARCH := $(shell uname -m)
ifeq ($(HOST_ARCH),x86_64)
HOST_GOARCH := amd64
else ifeq ($(HOST_ARCH),aarch64)
HOST_GOARCH := arm64
else ifeq ($(HOST_ARCH),arm64)
HOST_GOARCH := arm64
else
HOST_GOARCH :=
endif
DOCKER ?= docker
ISO_BUILDER_AMD64 ?= lankeeper-iso-builder-amd64
ISO_BUILDER_ARM64 ?= lankeeper-iso-builder-arm64

.PHONY: build test lint cyclo clean dev cross cross-amd64 cross-arm64 cross-all install iso iso-amd64 iso-arm64 iso-all docker-builder-amd64 docker-builder-arm64 docker-builders release release-archives release-amd64 release-arm64 release-all release-notes checksums sign check

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

# Every function stays at cyclomatic complexity 10 or below. gocyclo
# exits 1 when any function exceeds the threshold. CI runs this target,
# so the version is pinned here once for both.
GOCYCLO_VERSION := v0.6.0

cyclo:
	go run github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION) -over 10 .

clean:
	find $(DIST_DIR) -mindepth 1 -maxdepth 1 ! -name packages -exec rm -rf {} + 2>/dev/null || true

cross: cross-amd64

cross-amd64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(AMD64_BINARY) ./cmd/lankeeper

cross-arm64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(ARM64_BINARY) ./cmd/lankeeper

cross-all: cross-amd64 cross-arm64

install:
	@if [ -z "$(HOST_GOARCH)" ]; then \
		echo "unsupported host architecture: $(HOST_ARCH) (supported: x86_64, aarch64, arm64)" >&2; \
		exit 1; \
	fi
	$(MAKE) cross-$(HOST_GOARCH)
	sudo bash deploy/install.sh $(DIST_DIR)/$(BINARY)-linux-$(HOST_GOARCH)

docker-builder-amd64:
	$(DOCKER) build --platform linux/amd64 -t $(ISO_BUILDER_AMD64) -f deploy/iso/Dockerfile.build .

docker-builder-arm64:
	$(DOCKER) build --platform linux/arm64 -t $(ISO_BUILDER_ARM64) -f deploy/iso/Dockerfile.build .

docker-builders: docker-builder-amd64 docker-builder-arm64

# The builder runs as root and its entrypoint parses an ISO image with
# xorriso, fdisk and dd, so the repository is mounted by part rather than
# whole. The script reads configs/ and deploy/, and writes only the
# output image and the package cache under dist/. Mounting the root
# writable also exposed internal/, cmd/, web/ and .git, which would let a
# container compromise edit Go source the next host-side build compiles
# faithfully: a one-time break turned into a persistent one.
ISO_MOUNTS = \
	-v $(CURDIR)/configs:/build/configs:ro \
	-v $(CURDIR)/deploy:/build/deploy:ro \
	-v $(CURDIR)/$(DIST_DIR):/build/$(DIST_DIR)

$(DEBIAN_AMD64_ISO):
	mkdir -p $(dir $@)
	curl --fail --location --retry 3 --output $@.part $(DEBIAN_CDIMAGE)/amd64/iso-cd/$(notdir $@)
	mv $@.part $@

$(DEBIAN_ARM64_ISO):
	mkdir -p $(dir $@)
	curl --fail --location --retry 3 --output $@.part $(DEBIAN_CDIMAGE)/arm64/iso-cd/$(notdir $@)
	mv $@.part $@

iso: iso-amd64

iso-amd64: cross-amd64 docker-builder-amd64 $(DEBIAN_AMD64_ISO)
	@test -n "$(DEBIAN_AMD64_ISO)" || (echo "DEBIAN_AMD64_ISO or DEBIAN_ISO is required" >&2; exit 1)
	$(DOCKER) run --platform linux/amd64 --rm \
		$(ISO_MOUNTS) \
		-v $(CURDIR)/$(DEBIAN_AMD64_ISO):/debian.iso:ro \
		$(ISO_BUILDER_AMD64) /debian.iso /build/$(AMD64_BINARY) amd64 /build/$(AMD64_ISO) $(VERSION)

iso-arm64: cross-arm64 docker-builder-arm64 $(DEBIAN_ARM64_ISO)
	@test -n "$(DEBIAN_ARM64_ISO)" || (echo "DEBIAN_ARM64_ISO is required" >&2; exit 1)
	$(DOCKER) run --platform linux/arm64 --rm \
		$(ISO_MOUNTS) \
		-v $(CURDIR)/$(DEBIAN_ARM64_ISO):/debian.iso:ro \
		$(ISO_BUILDER_ARM64) /debian.iso /build/$(ARM64_BINARY) arm64 /build/$(ARM64_ISO) $(VERSION)

# iso-amd64 and iso-arm64 are independent: separate Docker images,
# separate ARCH-suffixed BUILD_DIR (/tmp/lankeeper-iso-build-$ARCH),
# separate dist/packages/{arch}/ caches, and they write to different
# output filenames in dist/. Build them concurrently to roughly halve
# wall time on multi-core hosts. -j 2 is forced on the recursive make
# so users who invoke `make iso-all` without -j still get parallelism.
iso-all:
	$(MAKE) -j 2 iso-amd64 iso-arm64

release: release-archives

release-archives: release-amd64 release-arm64
	$(MAKE) checksums VERSION=$(VERSION)
	$(MAKE) sign

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

release-all:
	# Single sub-make so the prerequisite graph is deduped (cross-amd64
	# and cross-arm64 are each built exactly once even though both
	# release-{arch} and iso-{arch} need them). iso-all is expanded
	# here as iso-amd64 + iso-arm64 to avoid spawning a nested make
	# that would re-trigger the phony cross- targets. -j 4 covers the
	# four leaf pipelines; checksums runs last because it hashes
	# every artifact produced above.
	$(MAKE) -j 4 release-amd64 release-arm64 iso-amd64 iso-arm64
	$(MAKE) checksums VERSION=$(VERSION)
	$(MAKE) sign

# The GitHub Release body is the hand-written CHANGELOG section for the
# version, never generated commit notes. The heading omits the tag's v,
# and the heading is matched as a literal prefix so the dots in a version
# are not read as regex wildcards. A missing or empty section is refused
# rather than published as a blank body.
release-notes:
	@mkdir -p dist
	@awk -v h="## [$(patsubst v%,%,$(VERSION))]" 'index($$0, h) == 1 {f = 1; next} f && /^## \[/ {exit} f {print}' CHANGELOG.md > dist/RELEASE_NOTES.md
	@if ! grep -q '[^[:space:]]' dist/RELEASE_NOTES.md; then \
	    rm -f dist/RELEASE_NOTES.md; \
	    echo "ERROR: CHANGELOG.md has no section for $(VERSION)" >&2; \
	    exit 1; \
	fi
	@echo "Release notes: dist/RELEASE_NOTES.md"

# `[ -f "$$f" ] && shasum ...` made the loop's exit status the status of
# its last iteration. `make release` builds tarballs and no ISOs, so the
# ISO pattern never matched, the final test was false, and the recipe
# failed after having written a perfectly good SHA256SUMS. An `if`
# reports success when its condition is false, so an absent artifact is
# no longer an error while a failing shasum still is.
#
# An empty result is refused rather than published: a SHA256SUMS that
# lists nothing looks like a release whose artifacts all verify.
checksums:
	@cd dist && { \
	    for f in $(BINARY)-$(VERSION)-linux-*.tar.gz $(BINARY)-$(VERSION)-installer-*.iso; do \
	        if [ -f "$$f" ]; then shasum -a 256 "$$f" || exit 1; fi; \
	    done; \
	} > SHA256SUMS
	@test -s dist/SHA256SUMS || { \
	    rm -f dist/SHA256SUMS; \
	    echo "ERROR: no release artifacts found for $(VERSION)" >&2; \
	    exit 1; \
	}
	@echo "Checksums: dist/SHA256SUMS"

# The router refuses an update whose SHA256SUMS carries no signature
# under the release key compiled into it, so a release without
# SHA256SUMS.sig cannot be installed. The private key never enters the
# repository; SIGNING_KEY points at it.
SIGNING_KEY ?= $(HOME)/.config/lankeeper/release-signing.key

sign:
	@test -f "$(SIGNING_KEY)" || { echo "ERROR: release signing key not found at $(SIGNING_KEY)" >&2; exit 1; }
	go run ./tools/signrelease -key "$(SIGNING_KEY)" dist/SHA256SUMS
	@echo "Signature: dist/SHA256SUMS.sig"

check:
	sudo bash deploy/install.sh --check
