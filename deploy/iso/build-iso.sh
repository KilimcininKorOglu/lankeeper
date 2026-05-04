#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

DEBIAN_ISO="${1:-}"
ARCH="${ARCH:-${3:-amd64}}"
BINARY_PATH="${2:-$PROJECT_ROOT/dist/home-router-linux-$ARCH}"
OUTPUT_ISO="${OUTPUT_ISO:-${4:-$PROJECT_ROOT/dist/home-router-installer-$ARCH.iso}}"
BUILD_DIR="${BUILD_DIR:-/tmp/home-router-iso-build-$ARCH}"
PACKAGE_REPO_DIR="${PACKAGE_REPO_DIR:-$PROJECT_ROOT/dist/packages/$ARCH}"

if [[ -z "$DEBIAN_ISO" ]]; then
    echo "Usage: $0 <debian-netinst.iso> [home-router-binary] [amd64|arm64] [output.iso]"
    echo ""
    echo "Example:"
    echo "  make cross-amd64"
    echo "  $0 debian-12.10.0-amd64-netinst.iso dist/home-router-linux-amd64 amd64"
    exit 1
fi

case "$ARCH" in
    amd64|arm64) ;;
    *)
        echo "ERROR: unsupported architecture: $ARCH" >&2
        echo "Supported architectures: amd64, arm64" >&2
        exit 1
        ;;
esac

if [[ ! -f "$DEBIAN_ISO" ]]; then
    echo "ERROR: Debian ISO not found: $DEBIAN_ISO"
    exit 1
fi

if [[ ! -f "$BINARY_PATH" ]]; then
    echo "ERROR: Binary not found: $BINARY_PATH"
    echo "Run 'make cross-$ARCH' first to build the Linux $ARCH binary."
    exit 1
fi

for cmd in apt-cache apt-get dpkg dpkg-scanpackages xorriso; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd is required. Install with: apt install $cmd"
        exit 1
    fi
done

APT_ARCH="$(dpkg --print-architecture)"
if [[ "$APT_ARCH" != "$ARCH" ]]; then
    echo "ERROR: apt architecture is $APT_ARCH, but ISO architecture is $ARCH" >&2
    echo "Run this script in a matching container, for example: docker run --platform linux/$ARCH ..." >&2
    exit 1
fi

PACKAGES=(
    ppp pppoe nftables wireguard-tools openvpn easy-rsa
    samba samba-common-bin smartmontools mdadm iproute2
    unbound dnsmasq rsyslog chrony qrencode
    wide-dhcpv6-client curl jq hdparm openssh-server
)

echo "=== Building Home Router Installer ISO ==="
echo "  Architecture: $ARCH"
echo "  Debian ISO: $DEBIAN_ISO"
echo "  Binary:     $BINARY_PATH"
echo "  Packages:   $PACKAGE_REPO_DIR"
echo "  Output:     $OUTPUT_ISO"
echo ""

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"
mkdir -p "$(dirname "$OUTPUT_ISO")"
mkdir -p "$PACKAGE_REPO_DIR"

echo "[1/7] Extracting Debian ISO..."
xorriso -osirrox on -indev "$DEBIAN_ISO" -extract / "$BUILD_DIR/iso" 2>/dev/null
chmod -R +w "$BUILD_DIR/iso"

echo "[2/7] Downloading required $ARCH packages with dependencies..."
pushd "$PACKAGE_REPO_DIR" >/dev/null

ALL_DEPS=$(apt-cache depends --recurse --no-recommends --no-suggests \
    --no-conflicts --no-breaks --no-replaces --no-enhances \
    "${PACKAGES[@]}" 2>/dev/null \
    | grep "^\w" | sort -u | grep -v "^<")

echo "  Resolving dependencies: $(echo "$ALL_DEPS" | wc -w) packages"
apt-get download $ALL_DEPS 2>/dev/null || {
    echo "NOTE: bulk download had errors, retrying individually..."
    for pkg in $ALL_DEPS; do
        apt-get download "$pkg" 2>/dev/null || true
    done
}

echo "  Downloaded $(ls -1 *.deb 2>/dev/null | wc -l) .deb files"
popd >/dev/null

echo "[3/7] Creating local package repository..."
pushd "$PACKAGE_REPO_DIR" >/dev/null
dpkg-scanpackages . /dev/null 2>/dev/null | gzip > Packages.gz
dpkg-scanpackages . /dev/null 2>/dev/null > Packages
popd >/dev/null
mkdir -p "$BUILD_DIR/iso/pool/extra"
cp "$PACKAGE_REPO_DIR"/*.deb "$BUILD_DIR/iso/pool/extra/"
cp "$PACKAGE_REPO_DIR"/Packages "$PACKAGE_REPO_DIR"/Packages.gz "$BUILD_DIR/iso/pool/extra/"

echo "[4/7] Adding home-router files..."
cp "$BINARY_PATH" "$BUILD_DIR/iso/home-router"
cp "$SCRIPT_DIR/preseed.cfg" "$BUILD_DIR/iso/"
cp "$SCRIPT_DIR/post-install.sh" "$BUILD_DIR/iso/"

# Validate critical assets exist before copying. A silent miss here used
# to produce a "successful" ISO that broke at install time.
required_yaml="$PROJECT_ROOT/configs/defaults/router.yaml"
if [[ ! -f "$required_yaml" ]]; then
    echo "ERROR: missing required default config: $required_yaml" >&2
    exit 1
fi

sysconf_tmpls=( "$PROJECT_ROOT/configs/sysconf"/*.tmpl )
if [[ ! -e "${sysconf_tmpls[0]}" ]]; then
    echo "ERROR: no sysconf templates found in $PROJECT_ROOT/configs/sysconf/" >&2
    exit 1
fi

required_units=(
    "$PROJECT_ROOT/deploy/systemd/home-router-agent.service"
    "$PROJECT_ROOT/deploy/systemd/home-router-web.service"
    "$PROJECT_ROOT/deploy/systemd/home-router.target"
)
for unit in "${required_units[@]}"; do
    if [[ ! -f "$unit" ]]; then
        echo "ERROR: missing required systemd unit: $unit" >&2
        exit 1
    fi
done

required_helper="$PROJECT_ROOT/deploy/dhcp-dns-update.sh"
if [[ ! -f "$required_helper" ]]; then
    echo "ERROR: missing required helper script: $required_helper" >&2
    exit 1
fi

mkdir -p "$BUILD_DIR/iso/configs/defaults"
cp "$PROJECT_ROOT/configs/defaults"/*.yaml "$BUILD_DIR/iso/configs/defaults/"

mkdir -p "$BUILD_DIR/iso/configs/sysconf"
cp "$PROJECT_ROOT/configs/sysconf"/*.tmpl "$BUILD_DIR/iso/configs/sysconf/"

mkdir -p "$BUILD_DIR/iso/systemd"
cp "${required_units[@]}" "$BUILD_DIR/iso/systemd/"

cp "$required_helper" "$BUILD_DIR/iso/dhcp-dns-update.sh"

echo "[5/7] Updating GRUB config..."
if [[ -f "$BUILD_DIR/iso/boot/grub/grub.cfg" ]]; then
    cp "$BUILD_DIR/iso/boot/grub/grub.cfg" "$BUILD_DIR/iso/boot/grub/grub.cfg.orig"
fi

BOOT_PATHS=(
    "/install.amd/vmlinuz:/install.amd/initrd.gz"
    "/install.a64/vmlinuz:/install.a64/initrd.gz"
    "/install/vmlinuz:/install/initrd.gz"
    "/vmlinuz:/initrd.gz"
)
KERNEL_PATH=""
INITRD_PATH=""
for pair in "${BOOT_PATHS[@]}"; do
    kernel="${pair%%:*}"
    initrd="${pair#*:}"
    if [[ -f "$BUILD_DIR/iso$kernel" && -f "$BUILD_DIR/iso$initrd" ]]; then
        KERNEL_PATH="$kernel"
        INITRD_PATH="$initrd"
        break
    fi
done

if [[ -z "$KERNEL_PATH" || -z "$INITRD_PATH" ]]; then
    echo "ERROR: could not find Debian installer kernel/initrd in extracted ISO" >&2
    exit 1
fi

sed \
    -e "s|__KERNEL_PATH__|$KERNEL_PATH|g" \
    -e "s|__INITRD_PATH__|$INITRD_PATH|g" \
    "$SCRIPT_DIR/grub.cfg" > "$BUILD_DIR/iso/boot/grub/grub.cfg"
echo "  Installer boot files: $KERNEL_PATH $INITRD_PATH"

# Fix EFI boot chain -- replace disk UUID search with cdrom search
if [[ -f "$BUILD_DIR/iso/EFI/debian/grub.cfg" ]]; then
    cat > "$BUILD_DIR/iso/EFI/debian/grub.cfg" <<'EFICFG'
search --set=root --file /home-router
set prefix=($root)/boot/grub
source $prefix/grub.cfg
EFICFG
fi

echo "[6/7] Updating isolinux config..."
if [[ -f "$BUILD_DIR/iso/isolinux/txt.cfg" ]]; then
    INSTALLER_PARAMS='priority=high locale?=en_US.UTF-8 keymap?=tr preseed/file=/cdrom/preseed.cfg'
    sed -i "s|append |append $INSTALLER_PARAMS |" "$BUILD_DIR/iso/isolinux/txt.cfg"
fi

echo "[7/7] Building ISO..."
case "$ARCH" in
    amd64)
        xorriso -as mkisofs \
            -r -V "HomeRouter" \
            -o "$OUTPUT_ISO" \
            -J -joliet-long \
            -isohybrid-mbr /usr/lib/ISOLINUX/isohdpfx.bin \
            -partition_offset 16 \
            -b isolinux/isolinux.bin \
            -c isolinux/boot.cat \
            -no-emul-boot -boot-load-size 4 -boot-info-table \
            -eltorito-alt-boot \
            -e boot/grub/efi.img \
            -no-emul-boot -isohybrid-gpt-basdat \
            "$BUILD_DIR/iso" 2>/dev/null
        ;;
    arm64)
        if ! command -v fdisk &>/dev/null; then
            echo "ERROR: fdisk is required for arm64 EFI partition extraction." >&2
            exit 1
        fi
        EFI_IMG="$BUILD_DIR/efi.img"
        PART_LINE="$(fdisk -l "$DEBIAN_ISO" | awk -v part="${DEBIAN_ISO}2" '$1 == part {print}')"
        if [[ -z "$PART_LINE" ]]; then
            echo "ERROR: could not find EFI partition in arm64 ISO: $DEBIAN_ISO" >&2
            exit 1
        fi
        read -r _part START_BLOCK _end BLOCK_COUNT _rest <<<"$PART_LINE"
        if [[ -z "${START_BLOCK:-}" || -z "${BLOCK_COUNT:-}" ]]; then
            echo "ERROR: could not parse EFI partition geometry: $PART_LINE" >&2
            exit 1
        fi
        dd if="$DEBIAN_ISO" bs=512 skip="$START_BLOCK" count="$BLOCK_COUNT" of="$EFI_IMG" status=none
        xorriso -as mkisofs \
            -r -V "HomeRouter" \
            -o "$OUTPUT_ISO" \
            -J -joliet-long \
            -e boot/grub/efi.img \
            -no-emul-boot \
            -append_partition 2 0xef "$EFI_IMG" \
            -partition_cyl_align all \
            "$BUILD_DIR/iso" 2>/dev/null
        ;;
esac

rm -rf "$BUILD_DIR"

echo ""
echo "=== ISO build complete ==="
echo "  Output: $OUTPUT_ISO"
echo "  Size:   $(du -h "$OUTPUT_ISO" | cut -f1)"
echo ""
echo "Write to USB: dd if=$OUTPUT_ISO of=/dev/sdX bs=4M status=progress"
