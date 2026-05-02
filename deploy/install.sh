#!/usr/bin/env bash
set -euo pipefail

BINARY_NAME="home-router"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/home-router"
DATA_DIR="/var/lib/home-router"
LOG_DIR="/var/log/home-router"
SYSTEMD_DIR="/etc/systemd/system"
SYSCTL_CONF="/etc/sysctl.d/99-home-router.conf"
SERVICE_USER="homerouter"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root"
        exit 1
    fi
}

check_debian() {
    if [[ ! -f /etc/debian_version ]]; then
        log_error "This script requires Debian-based system"
        exit 1
    fi
    local ver
    ver=$(cat /etc/debian_version)
    log_info "Detected Debian version: $ver"
}

install_dependencies() {
    log_info "Installing system dependencies..."
    apt-get update -qq

    apt-get install -y -qq \
        ppp pppoe \
        nftables \
        wireguard-tools \
        openvpn easy-rsa \
        samba samba-common-bin \
        smartmontools mdadm \
        iproute2 \
        unbound \
        dnsmasq \
        rsyslog \
        chrony \
        qrencode \
        wide-dhcpv6-client \
        curl \
        jq

    log_info "Dependencies installed"
}

create_user() {
    if id "$SERVICE_USER" &>/dev/null; then
        log_info "User $SERVICE_USER already exists"
        return
    fi

    useradd --system --no-create-home --home-dir /opt/home-router \
        --shell /usr/sbin/nologin "$SERVICE_USER"
    log_info "Created system user: $SERVICE_USER"
}

install_binary() {
    local binary_path="$1"

    if [[ ! -f "$binary_path" ]]; then
        log_error "Binary not found: $binary_path"
        exit 1
    fi

    cp "$binary_path" "$INSTALL_DIR/$BINARY_NAME"
    chmod +x "$INSTALL_DIR/$BINARY_NAME"
    log_info "Installed binary to $INSTALL_DIR/$BINARY_NAME"
}

setup_directories() {
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$DATA_DIR/tls"
    mkdir -p "$DATA_DIR/credentials"
    mkdir -p "$LOG_DIR"
    mkdir -p /var/log/unbound

    chown "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR"
    chown unbound:unbound /var/log/unbound 2>/dev/null || true

    log_info "Created directories"
}

install_systemd_units() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

    cp "$script_dir/systemd/home-router-agent.service" "$SYSTEMD_DIR/"
    cp "$script_dir/systemd/home-router-web.service" "$SYSTEMD_DIR/"
    cp "$script_dir/systemd/home-router.target" "$SYSTEMD_DIR/"

    systemctl daemon-reload
    systemctl enable home-router.target
    log_info "Installed and enabled systemd units"
}

setup_sysctl() {
    cat > "$SYSCTL_CONF" <<'SYSCTL'
# Home Router — network forwarding and hardening
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
net.ipv4.conf.all.rp_filter = 1
net.ipv4.conf.default.rp_filter = 1
net.ipv4.tcp_syncookies = 1
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv6.conf.all.accept_redirects = 0
net.ipv6.conf.default.accept_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.icmp_echo_ignore_broadcasts = 1
net.ipv4.icmp_ignore_bogus_error_responses = 1
SYSCTL

    sysctl -p "$SYSCTL_CONF" >/dev/null 2>&1
    log_info "Applied sysctl parameters"
}

setup_default_config() {
    if [[ -f "$CONFIG_DIR/router.yaml" ]]; then
        log_warn "Config already exists, skipping default config"
        return
    fi

    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local defaults_dir="$script_dir/../configs/defaults"

    if [[ -d "$defaults_dir" ]]; then
        cp "$defaults_dir"/*.yaml "$CONFIG_DIR/" 2>/dev/null || true
        log_info "Copied default config files"
    fi
}

setup_admin_password() {
    if [[ -f "$CONFIG_DIR/router.yaml" ]] && grep -q 'adminPasswordHash: "\$' "$CONFIG_DIR/router.yaml" 2>/dev/null; then
        log_info "Admin password already set"
        return
    fi

    echo ""
    echo "=== Initial Admin Setup ==="
    local password password_confirm
    read -rsp "Enter admin password: " password
    echo ""
    read -rsp "Confirm admin password: " password_confirm
    echo ""

    if [[ "$password" != "$password_confirm" ]]; then
        log_error "Passwords do not match"
        exit 1
    fi

    if [[ ${#password} -lt 8 ]]; then
        log_error "Password must be at least 8 characters"
        exit 1
    fi

    log_info "Admin password will be set on first start"
}

print_summary() {
    local version
    version=$("$INSTALL_DIR/$BINARY_NAME" version 2>/dev/null || echo "unknown")

    echo ""
    echo "========================================="
    echo "  Home Router Installation Complete"
    echo "========================================="
    echo ""
    echo "  Version:  $version"
    echo "  Config:   $CONFIG_DIR/router.yaml"
    echo "  Binary:   $INSTALL_DIR/$BINARY_NAME"
    echo "  Logs:     $LOG_DIR/"
    echo ""
    echo "  Start:    systemctl start home-router.target"
    echo "  Status:   systemctl status home-router.target"
    echo "  Logs:     journalctl -u home-router-web -f"
    echo ""
    echo "========================================="
}

main() {
    check_root
    check_debian

    local binary_path="${1:-./home-router}"

    install_dependencies
    create_user
    install_binary "$binary_path"
    setup_directories
    install_systemd_units
    setup_sysctl
    setup_default_config
    setup_admin_password
    print_summary
}

main "$@"
