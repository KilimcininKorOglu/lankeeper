#!/usr/bin/env bash
set -euo pipefail

BINARY_NAME="home-router"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/home-router"
DATA_DIR="/var/lib/home-router"
LOG_DIR="/var/log/home-router"
SYSTEMD_DIR="/etc/systemd/system"

echo "=== Home Router Post-Install ==="

# Install packages from local ISO repo
if [[ -d /cdrom/pool/extra ]] && [[ -f /cdrom/pool/extra/Packages ]]; then
    echo "deb [trusted=yes] file:///cdrom/pool extra/" > /etc/apt/sources.list.d/home-router-local.list
    apt-get update -qq 2>/dev/null || true
    apt-get install -y -qq \
        ppp pppoe nftables wireguard-tools openvpn easy-rsa \
        samba samba-common-bin smartmontools mdadm iproute2 \
        unbound dnsmasq rsyslog chrony qrencode \
        wide-dhcpv6-client curl jq hdparm \
        2>/dev/null || echo "WARN: some packages may not have installed"
    rm -f /etc/apt/sources.list.d/home-router-local.list
fi

# Install binary
cp /tmp/home-router "$INSTALL_DIR/$BINARY_NAME"
chmod +x "$INSTALL_DIR/$BINARY_NAME"

# Create directories
mkdir -p "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"
mkdir -p "$DATA_DIR/tls"
mkdir -p "$DATA_DIR/credentials"
mkdir -p "$DATA_DIR/backups"
mkdir -p "$LOG_DIR"
mkdir -p /var/log/unbound
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

# udev rules
cat > /etc/udev/rules.d/70-home-router.rules <<'UDEV'
SUBSYSTEM=="net", ACTION=="add", DRIVER=="rndis_host", NAME="usb0"
UDEV

# systemd units (embedded in binary or copied from ISO)
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

# Default config
if [[ ! -f "$CONFIG_DIR/router.yaml" ]]; then
    if [[ -d /tmp/configs/defaults ]]; then
        cp /tmp/configs/defaults/*.yaml "$CONFIG_DIR/"
        chmod 600 "$CONFIG_DIR"/*.yaml
    fi
fi

# Set admin password from installer
if [[ -f /tmp/admin-password.txt ]]; then
    ADMIN_PASS=$(cat /tmp/admin-password.txt)
    ADMIN_HASH=$("$INSTALL_DIR/$BINARY_NAME" hash-password "$ADMIN_PASS" 2>/dev/null || \
        python3 -c "import bcrypt; print(bcrypt.hashpw(b'$ADMIN_PASS', bcrypt.gensalt()).decode())" 2>/dev/null || \
        echo "")
    if [[ -n "$ADMIN_HASH" && -f "$CONFIG_DIR/router.yaml" ]]; then
        sed -i "s|adminPasswordHash:.*|adminPasswordHash: \"$ADMIN_HASH\"|" "$CONFIG_DIR/router.yaml"
        echo "Admin password set."
    fi
    rm -f /tmp/admin-password.txt
fi

# Set hostname from installer
HOSTNAME=$(hostname)
if [[ -n "$HOSTNAME" && -f "$CONFIG_DIR/router.yaml" ]]; then
    sed -i "s|hostname:.*|hostname: \"$HOSTNAME\"|" "$CONFIG_DIR/router.yaml"
fi

# First boot flag
touch "$DATA_DIR/.first-boot"

# Disable dnsmasq default (we manage it)
systemctl disable dnsmasq 2>/dev/null || true
systemctl stop dnsmasq 2>/dev/null || true

# SSH hardening — root login allowed (password set during install), LAN only via nftables
sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config
sed -i 's/#PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config

echo "=== Post-install complete ==="
echo "System will reboot into Home Router."
echo "Access web UI at https://<LAN_IP>:8443"
