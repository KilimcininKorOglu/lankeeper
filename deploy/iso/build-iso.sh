#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="/tmp/home-router-iso-build"
OUTPUT_ISO="$PROJECT_ROOT/home-router-installer.iso"

DEBIAN_ISO="${1:-}"
BINARY_PATH="${2:-$PROJECT_ROOT/home-router}"

if [[ -z "$DEBIAN_ISO" ]]; then
    echo "Usage: $0 <debian-netinst.iso> [home-router-binary]"
    echo ""
    echo "Example:"
    echo "  make cross"
    echo "  $0 debian-12.8.0-amd64-netinst.iso ./home-router"
    exit 1
fi

if [[ ! -f "$DEBIAN_ISO" ]]; then
    echo "ERROR: Debian ISO not found: $DEBIAN_ISO"
    exit 1
fi

if [[ ! -f "$BINARY_PATH" ]]; then
    echo "ERROR: Binary not found: $BINARY_PATH"
    echo "Run 'make cross' first to build the Linux amd64 binary."
    exit 1
fi

for cmd in xorriso dpkg-scanpackages; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd is required. Install with: apt install $cmd"
        exit 1
    fi
done

PACKAGES=(
    ppp pppoe nftables wireguard-tools openvpn easy-rsa
    samba samba-common-bin smartmontools mdadm iproute2
    unbound dnsmasq rsyslog chrony qrencode
    wide-dhcpv6-client curl jq hdparm
)

echo "=== Building Home Router Installer ISO ==="
echo "  Debian ISO: $DEBIAN_ISO"
echo "  Binary:     $BINARY_PATH"
echo "  Output:     $OUTPUT_ISO"
echo ""

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

echo "[1/7] Extracting Debian ISO..."
xorriso -osirrox on -indev "$DEBIAN_ISO" -extract / "$BUILD_DIR/iso" 2>/dev/null
chmod -R +w "$BUILD_DIR/iso"

echo "[2/7] Downloading required packages with dependencies..."
mkdir -p "$BUILD_DIR/iso/pool/extra"
pushd "$BUILD_DIR/iso/pool/extra" >/dev/null

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
pushd "$BUILD_DIR/iso" >/dev/null
dpkg-scanpackages pool/extra /dev/null 2>/dev/null | gzip > pool/extra/Packages.gz
dpkg-scanpackages pool/extra /dev/null 2>/dev/null > pool/extra/Packages
popd >/dev/null

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
cp "$SCRIPT_DIR/grub.cfg" "$BUILD_DIR/iso/boot/grub/grub.cfg"

# Fix EFI boot chain -- replace disk UUID search with cdrom search
if [[ -f "$BUILD_DIR/iso/EFI/debian/grub.cfg" ]]; then
    cat > "$BUILD_DIR/iso/EFI/debian/grub.cfg" <<'EFICFG'
search --set=root --file /home-router
set prefix=($root)/boot/grub
source $prefix/${grub_cpu}-efi/grub.cfg
EFICFG
fi

echo "[6/7] Updating isolinux config..."
if [[ -f "$BUILD_DIR/iso/isolinux/txt.cfg" ]]; then
    sed -i 's|append |append auto=true preseed/file=/cdrom/preseed.cfg |' "$BUILD_DIR/iso/isolinux/txt.cfg"
fi

echo "[7/7] Building ISO..."
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

rm -rf "$BUILD_DIR"

echo ""
echo "=== ISO build complete ==="
echo "  Output: $OUTPUT_ISO"
echo "  Size:   $(du -h "$OUTPUT_ISO" | cut -f1)"
echo ""
echo "Write to USB: dd if=$OUTPUT_ISO of=/dev/sdX bs=4M status=progress"
