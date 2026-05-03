#!/usr/bin/env bash
set -euo pipefail

# nullglob: unmatched globs expand to empty (not literal). Matches the
# pattern used in deploy/iso/post-install.sh for consistency.
shopt -s nullglob

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
        jq \
        hdparm

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
    mkdir -p "$DATA_DIR/backups"
    mkdir -p "$DATA_DIR/sysconf"
    mkdir -p "$LOG_DIR"
    mkdir -p /var/log/unbound

    chmod 750 "$CONFIG_DIR"
    chown root:"$SERVICE_USER" "$CONFIG_DIR"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR"
    chown unbound:unbound /var/log/unbound 2>/dev/null || true

    log_info "Created directories"
}

install_systemd_units() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

    cp "$script_dir/systemd/home-router-agent.service" "$SYSTEMD_DIR/"
    cp "$script_dir/systemd/home-router-web.service" "$SYSTEMD_DIR/"
    cp "$script_dir/systemd/home-router.target" "$SYSTEMD_DIR/"

    if ! systemctl daemon-reload; then
        log_warn "systemctl daemon-reload failed (chroot or no dbus)"
    fi
    if ! systemctl enable home-router.target; then
        log_warn "systemctl enable failed; will be enabled on first boot"
    fi
    log_info "Installed systemd units"
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

    if [[ ! -d "$defaults_dir" ]]; then
        log_error "Default configs directory not found: $defaults_dir"
        exit 1
    fi

    local default_yamls=( "$defaults_dir"/*.yaml )
    if [[ ${#default_yamls[@]} -eq 0 ]]; then
        log_error "No default YAML files found in $defaults_dir"
        exit 1
    fi
    cp "${default_yamls[@]}" "$CONFIG_DIR/"
    if [[ ! -f "$CONFIG_DIR/router.yaml" ]]; then
        log_error "router.yaml could not be installed from $defaults_dir"
        exit 1
    fi
    chmod 640 "$CONFIG_DIR"/*.yaml
    chown root:"$SERVICE_USER" "$CONFIG_DIR"/*.yaml
    log_info "Copied default config files"
}

ask_hostname() {
    echo ""
    echo "=== Ana Bilgisayar Adı / Hostname ==="
    local hostname
    read -rp "Ana bilgisayar adı girin / Enter hostname [hermes]: " hostname
    hostname="${hostname:-hermes}"

    sed -i "s|hostname:.*|hostname: \"$hostname\"|" "$CONFIG_DIR/router.yaml"
    hostnamectl set-hostname "$hostname" 2>/dev/null || true
    log_info "Ana bilgisayar adı ayarlandı / Hostname set to: $hostname"
}

ask_root_password() {
    echo ""
    echo "=== Root Şifresi / Root Password ==="
    local password password_confirm
    while true; do
        read -rsp "Root (SSH/konsol) şifresi girin (en az 8 karakter) / Enter root password (min 8 chars): " password
        echo ""
        if [[ ${#password} -lt 8 ]]; then
            log_error "Şifre en az 8 karakter olmalı / Password must be at least 8 characters"
            continue
        fi
        read -rsp "Root şifresini tekrar girin / Confirm root password: " password_confirm
        echo ""
        if [[ "$password" != "$password_confirm" ]]; then
            log_error "Şifreler eşleşmiyor / Passwords do not match"
            continue
        fi
        break
    done

    echo "root:$password" | chpasswd
    log_info "Root şifresi güncellendi / Root password updated"
}

ask_admin_password() {
    if [[ -f "$CONFIG_DIR/router.yaml" ]] && grep -q 'adminPasswordHash: "\$' "$CONFIG_DIR/router.yaml" 2>/dev/null; then
        log_info "Yönetici şifresi zaten ayarlı / Admin password already set"
        return
    fi

    echo ""
    echo "=== Web Arayüzü Yönetici Şifresi / Web UI Admin Password ==="
    local password password_confirm
    while true; do
        read -rsp "Web arayüzü yönetici şifresi girin (en az 8 karakter) / Enter web UI admin password (min 8 chars): " password
        echo ""
        if [[ ${#password} -lt 8 ]]; then
            log_error "Şifre en az 8 karakter olmalı / Password must be at least 8 characters"
            continue
        fi
        read -rsp "Yönetici şifresini tekrar girin / Confirm admin password: " password_confirm
        echo ""
        if [[ "$password" != "$password_confirm" ]]; then
            log_error "Şifreler eşleşmiyor / Passwords do not match"
            continue
        fi
        break
    done

    local hash
    hash=$("$INSTALL_DIR/$BINARY_NAME" hash-password "$password" 2>/dev/null) || hash=""

    if [[ -n "$hash" ]]; then
        sed -i "s|adminPasswordHash:.*|adminPasswordHash: \"$hash\"|" "$CONFIG_DIR/router.yaml"
        log_info "Yönetici şifresi yapılandırmaya yazıldı / Admin password hash written to config"
    else
        log_error "Şifre hash'lenemedi (home-router hash-password başarısız) / Failed to hash password"
        exit 1
    fi
}

ask_timezone() {
    echo ""
    echo "=== Saat Dilimi / Timezone ==="
    echo "  1) Europe/Istanbul (default)"
    echo "  2) Europe/London"
    echo "  3) Europe/Berlin"
    echo "  4) America/New_York"
    echo "  5) America/Los_Angeles"
    echo "  6) Asia/Tokyo"
    echo "  7) UTC"
    local choice
    read -rp "Saat dilimi seçin / Select timezone [1]: " choice
    choice="${choice:-1}"

    local tz
    case "$choice" in
        1) tz="Europe/Istanbul" ;;
        2) tz="Europe/London" ;;
        3) tz="Europe/Berlin" ;;
        4) tz="America/New_York" ;;
        5) tz="America/Los_Angeles" ;;
        6) tz="Asia/Tokyo" ;;
        7) tz="UTC" ;;
        *) tz="Europe/Istanbul" ;;
    esac

    sed -i "s|timezone:.*|timezone: \"$tz\"|" "$CONFIG_DIR/router.yaml"
    timedatectl set-timezone "$tz" 2>/dev/null || true
    log_info "Saat dilimi ayarlandı / Timezone set to: $tz"
}

ask_language() {
    echo ""
    echo "=== Web Arayüzü Dili / Web UI Language ==="
    echo "  1) English (en) (default)"
    echo "  2) Türkçe (tr)"
    local choice
    read -rp "Dil seçin / Select language [1]: " choice
    choice="${choice:-1}"

    local lang
    case "$choice" in
        1) lang="en" ;;
        2) lang="tr" ;;
        *) lang="en" ;;
    esac

    sed -i "s|^  language:.*|  language: \"$lang\"|" "$CONFIG_DIR/router.yaml"
    log_info "Web arayüzü dili ayarlandı / Language set to: $lang"
}

ask_keyboard() {
    echo ""
    echo "=== Klavye Düzeni / Keyboard Layout ==="
    echo "  1) tr — Turkish Q (default)"
    echo "  2) us — US English"
    echo "  3) de — German"
    echo "  4) fr — French"
    echo "  5) uk — UK English"
    local choice
    read -rp "Klavye düzeni seçin / Select keyboard layout [1]: " choice
    choice="${choice:-1}"

    local kb
    case "$choice" in
        1) kb="tr" ;;
        2) kb="us" ;;
        3) kb="de" ;;
        4) kb="fr" ;;
        5) kb="uk" ;;
        *) kb="tr" ;;
    esac

    if localectl set-keymap "$kb" 2>/dev/null; then
        log_info "Klavye düzeni ayarlandı / Keyboard layout set to: $kb"
    elif loadkeys "$kb" 2>/dev/null; then
        log_warn "loadkeys ile geçici ayarlandı (kalıcı değil) / set with loadkeys (not persistent): $kb"
    else
        log_warn "Klavye düzeni ayarlanamadı / Failed to set keyboard layout: $kb"
    fi
}

print_summary() {
    local version
    version=$("$INSTALL_DIR/$BINARY_NAME" version 2>/dev/null || echo "unknown")

    echo ""
    echo "============================================="
    echo "  Home Router Kurulumu Tamamlandı"
    echo "  Home Router Installation Complete"
    echo "============================================="
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
    echo "  Web arayüzü / Web UI: https://<LAN_IP>:8443"
    echo ""
    echo "============================================="
}

setup_dhcp_dns_script() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

    if [[ ! -f "$script_dir/dhcp-dns-update.sh" ]]; then
        log_warn "dhcp-dns-update.sh not found at $script_dir, skipping"
        return
    fi

    mkdir -p /usr/local/lib/home-router
    cp "$script_dir/dhcp-dns-update.sh" /usr/local/lib/home-router/
    chmod +x /usr/local/lib/home-router/dhcp-dns-update.sh
    log_info "Installed DHCP-DNS update script"
}

setup_udev_rules() {
    cat > /etc/udev/rules.d/70-home-router.rules <<'UDEV'
# USB tethering — Android RNDIS
SUBSYSTEM=="net", ACTION=="add", DRIVER=="rndis_host", NAME="usb0"
UDEV

    udevadm control --reload-rules
    log_info "Installed udev rules"
}

setup_sysconf_templates() {
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local sysconf_dir="$script_dir/../configs/sysconf"

    if [[ ! -d "$sysconf_dir" ]]; then
        log_warn "sysconf directory not found: $sysconf_dir"
        return
    fi
    local tmpls=( "$sysconf_dir"/*.tmpl )
    if [[ ${#tmpls[@]} -eq 0 ]]; then
        log_warn "No sysconf templates in $sysconf_dir"
        return
    fi
    mkdir -p "$DATA_DIR/sysconf"
    cp "${tmpls[@]}" "$DATA_DIR/sysconf/"
    log_info "Installed sysconf templates"
}

setup_initial_tls() {
    if [[ -f "$DATA_DIR/tls/server.crt" ]]; then
        log_info "TLS certificate already exists"
        return
    fi

    log_info "Generating initial self-signed TLS certificate..."
    if ! "$INSTALL_DIR/$BINARY_NAME" gen-cert \
            --config "$CONFIG_DIR/router.yaml" \
            --data-dir "$DATA_DIR" >/dev/null; then
        log_error "TLS certificate generation failed"
        exit 1
    fi
    chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR/tls"
    chmod 600 "$DATA_DIR/tls"/*.key
    chmod 644 "$DATA_DIR/tls"/*.crt
    log_info "TLS certificate generated"
}

check_installation() {
    local errors=0

    echo "=== Installation Check ==="

    for cmd in pppd nft wg openvpn unbound dnsmasq chronyc smbcontrol smartctl mdadm qrencode; do
        if command -v "$cmd" &>/dev/null; then
            echo "  [OK] $cmd"
        else
            echo "  [MISSING] $cmd"
            ((errors++))
        fi
    done

    if [[ -f "$INSTALL_DIR/$BINARY_NAME" ]]; then
        echo "  [OK] $BINARY_NAME binary"
    else
        echo "  [MISSING] $BINARY_NAME binary"
        ((errors++))
    fi

    for unit in home-router-agent.service home-router-web.service home-router.target; do
        if [[ -f "$SYSTEMD_DIR/$unit" ]]; then
            echo "  [OK] $unit"
        else
            echo "  [MISSING] $unit"
            ((errors++))
        fi
    done

    if [[ -f "$CONFIG_DIR/router.yaml" ]]; then
        echo "  [OK] router.yaml"
    else
        echo "  [MISSING] router.yaml"
        ((errors++))
    fi

    if [[ "$errors" -eq 0 ]]; then
        echo ""
        echo "All checks passed."
    else
        echo ""
        echo "$errors issue(s) found."
    fi

    return "$errors"
}

main() {
    if [[ "${1:-}" == "--check" ]]; then
        check_installation
        exit $?
    fi

    check_root
    check_debian

    local binary_path="${1:-./home-router}"

    install_dependencies
    create_user
    install_binary "$binary_path"
    setup_directories
    install_systemd_units
    setup_sysctl
    setup_udev_rules
    setup_dhcp_dns_script
    setup_sysconf_templates
    setup_default_config
    ask_hostname
    ask_root_password
    ask_admin_password
    ask_timezone
    ask_language
    ask_keyboard
    setup_initial_tls
    print_summary
}

main "$@"
