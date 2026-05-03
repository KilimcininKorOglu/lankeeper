#!/usr/bin/env bash
set -euo pipefail

BINARY_NAME="home-router"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/home-router"
DATA_DIR="/var/lib/home-router"
LOG_DIR="/var/log/home-router"
SYSTEMD_DIR="/etc/systemd/system"
SERVICE_USER="homerouter"

echo "=== Home Router Kurulum Sonrası / Post-Install ==="

# Install packages from local ISO repo
if [[ -d /cdrom/pool/extra ]] && [[ -f /cdrom/pool/extra/Packages ]]; then
    echo "deb [trusted=yes] file:///cdrom/pool extra/" > /etc/apt/sources.list.d/home-router-local.list
    apt-get update -qq || true
    apt-get install -y -qq \
        ppp pppoe nftables wireguard-tools openvpn easy-rsa \
        samba samba-common-bin smartmontools mdadm iproute2 \
        unbound dnsmasq rsyslog chrony qrencode \
        wide-dhcpv6-client curl jq hdparm \
        || echo "UYARI / WARN: Bazı paketler kurulamadı / Some packages may not have installed"
    rm -f /etc/apt/sources.list.d/home-router-local.list
fi

# Ensure homerouter system user exists (d-i creates an interactive user; if
# missing for any reason, fall back to a system account).
if ! id "$SERVICE_USER" &>/dev/null; then
    useradd --system --no-create-home --home-dir /opt/home-router \
        --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# Install binary
cp /tmp/home-router "$INSTALL_DIR/$BINARY_NAME"
chmod +x "$INSTALL_DIR/$BINARY_NAME"

# Create directories with correct ownership for the homerouter service user.
# The web service runs as homerouter and must be able to write TLS certs,
# credentials, backups, and logs.
mkdir -p "$CONFIG_DIR"
chmod 750 "$CONFIG_DIR"
chown root:"$SERVICE_USER" "$CONFIG_DIR" 2>/dev/null || true
mkdir -p "$DATA_DIR/tls"
mkdir -p "$DATA_DIR/credentials"
mkdir -p "$DATA_DIR/backups"
mkdir -p "$DATA_DIR/sysconf"
mkdir -p "$LOG_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR" 2>/dev/null || true
chown -R "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR" 2>/dev/null || true
mkdir -p /var/log/unbound
chown unbound:unbound /var/log/unbound 2>/dev/null || true
mkdir -p /var/log/chrony

# sysctl
cat > /etc/sysctl.d/99-home-router.conf <<'SYSCTL'
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

# Apply sysctl immediately
sysctl -p /etc/sysctl.d/99-home-router.conf >/dev/null 2>&1 || true

# udev rules
cat > /etc/udev/rules.d/70-home-router.rules <<'UDEV'
# USB tethering — Android RNDIS
SUBSYSTEM=="net", ACTION=="add", DRIVER=="rndis_host", NAME="usb0"
UDEV
udevadm control --reload-rules 2>/dev/null || true

# DHCP-DNS update helper script
if [[ -f /tmp/dhcp-dns-update.sh ]]; then
    mkdir -p /usr/local/lib/home-router
    cp /tmp/dhcp-dns-update.sh /usr/local/lib/home-router/
    chmod +x /usr/local/lib/home-router/dhcp-dns-update.sh
fi

# Sysconf templates (used by services to render /etc configs at runtime)
if [[ -d /tmp/configs/sysconf ]]; then
    cp /tmp/configs/sysconf/*.tmpl "$DATA_DIR/sysconf/" 2>/dev/null || true
    chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR/sysconf" 2>/dev/null || true
fi

# systemd units (copied from ISO or embedded inline as fallback)
if [[ -d /tmp/systemd ]]; then
    cp /tmp/systemd/*.service "$SYSTEMD_DIR/"
    cp /tmp/systemd/*.target "$SYSTEMD_DIR/"
else
    cat > "$SYSTEMD_DIR/home-router-agent.service" <<'UNIT'
[Unit]
Description=Home Router Privileged Agent
PartOf=home-router.target
After=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/home-router agent
Restart=always
RestartSec=3
User=root
RuntimeDirectory=home-router
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=home-router.target
UNIT

    cat > "$SYSTEMD_DIR/home-router-web.service" <<'UNIT'
[Unit]
Description=Home Router Web Server
PartOf=home-router.target
After=home-router-agent.service
Requires=home-router-agent.service

[Service]
Type=simple
ExecStart=/usr/local/bin/home-router serve
Restart=always
RestartSec=3
User=homerouter
AmbientCapabilities=CAP_NET_BIND_SERVICE
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/etc/home-router /var/lib/home-router /var/log/home-router

[Install]
WantedBy=home-router.target
UNIT

    cat > "$SYSTEMD_DIR/home-router.target" <<'UNIT'
[Unit]
Description=Home Router Services
After=network-online.target
Wants=home-router-agent.service home-router-web.service

[Install]
WantedBy=multi-user.target
UNIT
fi

# Enable services
systemctl daemon-reload
systemctl enable home-router.target

# Default config — copy YAML defaults from ISO. Without these, router.yaml
# would never exist and the admin password / hostname sed steps below would
# silently no-op.
if [[ ! -f "$CONFIG_DIR/router.yaml" ]]; then
    if [[ -d /tmp/configs/defaults ]]; then
        cp /tmp/configs/defaults/*.yaml "$CONFIG_DIR/"
        chmod 640 "$CONFIG_DIR"/*.yaml
        chown root:"$SERVICE_USER" "$CONFIG_DIR"/*.yaml 2>/dev/null || true
    else
        echo "UYARI / WARN: Default config'ler bulunamadı / Default configs not found at /tmp/configs/defaults"
    fi
fi

# Set admin password from installer
if [[ -f /tmp/admin-password.txt ]]; then
    ADMIN_PASS=$(cat /tmp/admin-password.txt)
    ADMIN_HASH=$("$INSTALL_DIR/$BINARY_NAME" hash-password "$ADMIN_PASS" 2>/dev/null || echo "")
    if [[ -n "$ADMIN_HASH" && -f "$CONFIG_DIR/router.yaml" ]]; then
        sed -i "s|adminPasswordHash:.*|adminPasswordHash: \"$ADMIN_HASH\"|" "$CONFIG_DIR/router.yaml"
        echo "Yönetici şifresi ayarlandı. / Admin password set."
    fi
    rm -f /tmp/admin-password.txt
fi

# Set hostname — prefer the user-supplied value from the early_command
# debconf prompt over the system hostname (which is hermes by default).
if [[ -f /tmp/hostname.txt ]]; then
    HOSTNAME_VAL=$(cat /tmp/hostname.txt)
    rm -f /tmp/hostname.txt
else
    HOSTNAME_VAL=$(hostname)
fi
if [[ -n "$HOSTNAME_VAL" && -f "$CONFIG_DIR/router.yaml" ]]; then
    sed -i "s|hostname:.*|hostname: \"$HOSTNAME_VAL\"|" "$CONFIG_DIR/router.yaml"
    hostnamectl set-hostname "$HOSTNAME_VAL" 2>/dev/null || \
        echo "$HOSTNAME_VAL" > /etc/hostname
fi

# Propagate timezone selected during d-i to router.yaml.
if [[ -f /etc/timezone ]] && [[ -f "$CONFIG_DIR/router.yaml" ]]; then
    TZ_VAL=$(cat /etc/timezone)
    if [[ -n "$TZ_VAL" ]]; then
        sed -i "s|timezone:.*|timezone: \"$TZ_VAL\"|" "$CONFIG_DIR/router.yaml"
    fi
fi

# Propagate locale to router.yaml language field. The web UI only ships
# tr and en; pick "tr" for Turkish locales, otherwise default to "en".
if [[ -f "$CONFIG_DIR/router.yaml" ]]; then
    LOCALE_VAL=""
    if [[ -f /etc/default/locale ]]; then
        LOCALE_VAL=$(. /etc/default/locale 2>/dev/null; echo "${LANG:-}")
    fi
    case "$LOCALE_VAL" in
        tr_*) LANG_CODE="tr" ;;
        *)    LANG_CODE="en" ;;
    esac
    sed -i "s|^  language:.*|  language: \"$LANG_CODE\"|" "$CONFIG_DIR/router.yaml"
fi

# First boot flag
touch "$DATA_DIR/.first-boot"

# Disable dnsmasq default (we manage it)
systemctl disable dnsmasq 2>/dev/null || true
systemctl stop dnsmasq 2>/dev/null || true

# SSH hardening — root login allowed (password set during install), LAN only via nftables
sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config
sed -i 's/#PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config

# Generate initial self-signed TLS certificate so the web service can start
# immediately on first boot. Run the binary as root briefly; EnsureTLSCert()
# fires before ListenAndServeTLS so the cert is written even if the bind
# fails (target system has no LAN IP yet). Then transfer ownership to the
# service user.
if [[ ! -f "$DATA_DIR/tls/server.crt" ]] && [[ -f "$CONFIG_DIR/router.yaml" ]]; then
    "$INSTALL_DIR/$BINARY_NAME" serve --config "$CONFIG_DIR/router.yaml" >/dev/null 2>&1 &
    TLS_PID=$!
    sleep 2
    kill "$TLS_PID" 2>/dev/null || true
    wait "$TLS_PID" 2>/dev/null || true
    if [[ -f "$DATA_DIR/tls/server.crt" ]]; then
        chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR/tls" 2>/dev/null || true
        chmod 600 "$DATA_DIR/tls"/*.key 2>/dev/null || true
        chmod 644 "$DATA_DIR/tls"/*.crt 2>/dev/null || true
    fi
fi

echo "=== Kurulum tamamlandı / Post-install complete ==="
echo "Sistem Home Router olarak yeniden başlatılacak. / System will reboot into Home Router."
echo "Web arayüzü: https://<LAN_IP>:8443 / Web UI: https://<LAN_IP>:8443"
