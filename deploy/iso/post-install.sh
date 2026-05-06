#!/bin/sh
set -eu

BINARY_NAME="lankeeper"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/lankeeper"
DATA_DIR="/var/lib/lankeeper"
LOG_DIR="/var/log/lankeeper"
SYSTEMD_DIR="/etc/systemd/system"
SERVICE_USER="lankeeper"

echo "=== LANKeeper Kurulum Sonrası / Post-Install ==="

# Install packages from the local ISO repo copied by late_command. Keep apt
# pointed only at this flat repo so an offline router install never prompts
# for a Debian CD label or network mirror.
if [ -d /tmp/pool-extra ] && [ -f /tmp/pool-extra/Packages ]; then
    cp /etc/apt/sources.list /etc/apt/sources.list.lankeeper.bak 2>/dev/null || true
    mkdir -p /etc/apt/sources.list.d
    rm -f /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources
    echo "deb [trusted=yes] file:/tmp/pool-extra ./" > /etc/apt/sources.list
    apt-get update -qq
    # Debian "Standard system utilities" task (minus systemd-timesyncd
    # and inetutils-telnet) + LANKeeper-specific packages. Replicated
    # manually because preseed.cfg disables tasksel for offline
    # installs. Keep this list aligned with STANDARD_TASK_PACKAGES and
    # LANKEEPER_PACKAGES in deploy/iso/build-iso.sh.
    apt-get install -y -qq \
        apt-listchanges apt-utils bash-completion bind9-dnsutils bind9-host \
        bzip2 ca-certificates cpio cron cron-daemon-common debconf-i18n \
        debian-faq doc-debian fdisk file gettext-base groff-base ifupdown \
        init iputils-ping isc-dhcp-client isc-dhcp-common \
        kmod krb5-locales less libc-l10n liblockfile-bin libnss-systemd \
        libpam-systemd locales logrotate lsof man-db manpages media-types \
        mime-support nano ncurses-term netbase netcat-traditional \
        openssh-client pciutils perl procps python3-reportbug \
        readline-common reportbug sensible-utils systemd systemd-sysv \
        traceroute ucf udev vim-common vim-tiny wamerican \
        wget whiptail xz-utils \
        bash dbus ppp pppoe nftables wireguard-tools openvpn easy-rsa \
        samba samba-common-bin smartmontools mdadm iproute2 \
        unbound dnsmasq rsyslog chrony qrencode \
        wide-dhcpv6-client curl jq hdparm openssh-server htop
    cat > /etc/apt/sources.list <<'APT_SOURCES'
deb http://deb.debian.org/debian bookworm main
deb http://deb.debian.org/debian bookworm-updates main
deb http://security.debian.org/debian-security bookworm-security main
APT_SOURCES
fi

# Ensure the system message bus is enabled and started. Without dbus the
# systemd CLIs (hostnamectl, timedatectl, localectl) fail with
# "Failed to connect to bus" and the hostnamectl call later in this
# script silently degrades to /etc/hostname only. systemctl enable starts
# at next boot; --now starts it immediately so the steps below succeed.
systemctl unmask dbus.service dbus.socket 2>/dev/null || true
systemctl enable --now dbus.socket 2>/dev/null || true
systemctl enable --now dbus.service 2>/dev/null || true

# Mask systemd-timesyncd defensively. The package is intentionally
# excluded from the offline repo (chrony owns NTP), but a future
# `apt install` from the public mirror could bring it in. Masking now
# guarantees chrony stays the sole NTP client.
systemctl mask systemd-timesyncd.service 2>/dev/null || true

# Set timezone chosen in the installer. This avoids depending on d-i's
# time/zone template availability during CD preseed loading.
if [ -s /tmp/timezone.txt ]; then
    ROUTER_TZ=$(cat /tmp/timezone.txt)
    if [ -f "/usr/share/zoneinfo/$ROUTER_TZ" ]; then
        echo "$ROUTER_TZ" > /etc/timezone
        ln -sf "/usr/share/zoneinfo/$ROUTER_TZ" /etc/localtime
    else
        echo "HATA / ERROR: Geçersiz saat dilimi / invalid timezone: $ROUTER_TZ" >&2
        exit 1
    fi
fi

# d-i creates the lankeeper user via passwd/make-user=true. If for some
# reason the user is missing here, abort — installing the service with no
# owner is worse than failing loudly.
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    echo "HATA / ERROR: $SERVICE_USER kullanıcısı yok / user $SERVICE_USER missing" >&2
    exit 1
fi

# Install binary
cp /tmp/lankeeper "$INSTALL_DIR/$BINARY_NAME"
chmod +x "$INSTALL_DIR/$BINARY_NAME"

# Create directories with correct ownership for the lankeeper service user.
# The web service runs as lankeeper and must be able to write TLS certs,
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
cat > /etc/sysctl.d/99-lankeeper.conf <<'SYSCTL'
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
sysctl -p /etc/sysctl.d/99-lankeeper.conf >/dev/null 2>&1 || true

# udev rules
cat > /etc/udev/rules.d/70-lankeeper.rules <<'UDEV'
# USB tethering — Android RNDIS
SUBSYSTEM=="net", ACTION=="add", DRIVER=="rndis_host", NAME="usb0"
UDEV
udevadm control --reload-rules 2>/dev/null || true

# DHCP-DNS update helper script
if [ -f /tmp/dhcp-dns-update.sh ]; then
    mkdir -p /usr/local/lib/lankeeper
    cp /tmp/dhcp-dns-update.sh /usr/local/lib/lankeeper/
    chmod +x /usr/local/lib/lankeeper/dhcp-dns-update.sh
fi

# Sysconf templates — service code calls
# template.ParseFiles("configs/sysconf/...") relative to CWD, so mirror
# that path under $DATA_DIR. render-configs chdirs to $DATA_DIR.
if [ -d /tmp/configs/sysconf ]; then
    set -- /tmp/configs/sysconf/*.tmpl
    if [ -e "$1" ]; then
        mkdir -p "$DATA_DIR/configs/sysconf"
        cp "$@" "$DATA_DIR/configs/sysconf/"
        chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR/configs" 2>/dev/null || true
    else
        echo "UYARI / WARN: sysconf şablonları boş / sysconf templates empty"
    fi
fi

# systemd units — copied from /tmp/systemd which late_command populated
# from /cdrom/systemd. Fail loudly if missing rather than silently using
# divergent inline fallbacks.
set -- /tmp/systemd/*.service
if [ ! -d /tmp/systemd ] || [ ! -e "$1" ]; then
    echo "HATA / ERROR: systemd unit dosyaları bulunamadı (/tmp/systemd) / systemd unit files missing" >&2
    exit 1
fi
cp "$@" "$SYSTEMD_DIR/"
set -- /tmp/systemd/*.target
[ -e "$1" ] && cp "$@" "$SYSTEMD_DIR/"

# Enable services. systemctl in d-i chroot can fail in unusual setups; tolerate.
systemctl daemon-reload 2>/dev/null || true
systemctl enable lankeeper.target 2>/dev/null || true

# Default config — copy YAML defaults from ISO. Without these, router.yaml
# would never exist and the admin password / hostname sed steps below would
# silently no-op.
if [ ! -f "$CONFIG_DIR/router.yaml" ]; then
    set -- /tmp/configs/defaults/*.yaml
    if [ -e "$1" ]; then
        cp "$@" "$CONFIG_DIR/"
        chmod 640 "$CONFIG_DIR"/*.yaml
        chown root:"$SERVICE_USER" "$CONFIG_DIR"/*.yaml 2>/dev/null || true
    else
        echo "UYARI / WARN: Default config'ler bulunamadı / Default configs not found at /tmp/configs/defaults"
    fi
fi

# Set admin password from installer
if [ -f /tmp/admin-password.txt ]; then
    ADMIN_PASS=$(cat /tmp/admin-password.txt)
    if [ -z "$ADMIN_PASS" ]; then
        echo "UYARI / WARN: Yönetici şifresi boş, ayarlanmadı / Admin password empty, not set"
    else
        ADMIN_HASH=$("$INSTALL_DIR/$BINARY_NAME" hash-password "$ADMIN_PASS" 2>/dev/null || echo "")
        if [ -z "$ADMIN_HASH" ]; then
            echo "HATA / ERROR: Şifre hash'lenemedi / Failed to hash admin password"
        elif [ ! -f "$CONFIG_DIR/router.yaml" ]; then
            echo "HATA / ERROR: router.yaml yok, şifre yazılamadı / router.yaml missing, password not written"
        else
            sed -i "s|adminPasswordHash:.*|adminPasswordHash: \"$ADMIN_HASH\"|" "$CONFIG_DIR/router.yaml"
            echo "Yönetici şifresi ayarlandı. / Admin password set."
        fi
    fi
    rm -f /tmp/admin-password.txt
fi

# Set hostname — prefer the user-supplied value from the early_command
# debconf prompt over the system hostname (which is hermes by default).
if [ -f /tmp/hostname.txt ]; then
    HOSTNAME_VAL=$(cat /tmp/hostname.txt)
    rm -f /tmp/hostname.txt
else
    HOSTNAME_VAL=$(hostname)
fi
if [ -n "$HOSTNAME_VAL" ] && [ -f "$CONFIG_DIR/router.yaml" ]; then
    sed -i "s|hostname:.*|hostname: \"$HOSTNAME_VAL\"|" "$CONFIG_DIR/router.yaml"
    hostnamectl set-hostname "$HOSTNAME_VAL" 2>/dev/null || \
        echo "$HOSTNAME_VAL" > /etc/hostname
fi

# Propagate timezone selected during d-i to router.yaml.
if [ -f /etc/timezone ] && [ -f "$CONFIG_DIR/router.yaml" ]; then
    TZ_VAL=$(cat /etc/timezone)
    if [ -n "$TZ_VAL" ]; then
        sed -i "s|timezone:.*|timezone: \"$TZ_VAL\"|" "$CONFIG_DIR/router.yaml"
    fi
fi

# Propagate locale to router.yaml language field. The web UI only ships
# tr and en; pick "tr" for Turkish locales, otherwise default to "en".
if [ -f "$CONFIG_DIR/router.yaml" ]; then
    LOCALE_VAL=""
    if [ -f /etc/default/locale ]; then
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

# (Native servis enable for-loop'u render-configs sonrasına taşındı)

# Bootstrap firewall — write a static /etc/nftables.conf and enable
# nftables.service. Loaded by the kernel before sshd starts (Debian's
# nftables.service orders Before=network-pre.target), so port 22 is
# never reachable from a non-LAN source even during the boot transient
# before the lankeeper agent applies the live (template-rendered)
# ruleset. The agent's Apply() overwrites this with the full ruleset
# once the user assigns interface roles via the web UI; until then this
# bootstrap is the safety net.
#
# Assumes the canonical default LAN subnet 10.10.10.0/24 (router.yaml
# default + first-boot bridge in internal/web/firstboot.go). Custom LAN
# subnets must reach the live ruleset stage before the bootstrap matters.
cat > /etc/nftables.conf <<'NFT'
#!/usr/sbin/nft -f
# LANKeeper bootstrap ruleset — pre-agent boot safety net.
# Replaced at runtime by the rendered template once the agent applies.

flush ruleset

table inet bootstrap {
    chain input {
        type filter hook input priority 0; policy drop;

        ct state established,related accept
        ct state invalid drop
        iifname "lo" accept

        # Allow the canonical LAN subnet (default 10.10.10.0/24).
        ip saddr 10.10.10.0/24 accept

        # Minimal ICMP for diagnostics.
        ip protocol icmp icmp type { echo-request, echo-reply, destination-unreachable, time-exceeded } accept
        ip6 nexthdr icmpv6 icmpv6 type {
            destination-unreachable,
            packet-too-big,
            time-exceeded,
            parameter-problem,
            echo-request,
            echo-reply,
            nd-router-solicit,
            nd-router-advert,
            nd-neighbor-solicit,
            nd-neighbor-advert
        } accept

        log prefix "BOOTSTRAP_DROP: " drop
    }

    chain forward {
        type filter hook forward priority 0; policy accept;
    }

    chain output {
        type filter hook output priority 0; policy accept;
    }
}
NFT
chmod 644 /etc/nftables.conf
systemctl enable nftables.service 2>/dev/null || true

# GRUB branding — Debian's generated menu defaults to "Debian GNU/Linux".
# Set the distributor explicitly so the installed disk boots as LANKeeper.
mkdir -p /etc/default/grub.d
cat > /etc/default/grub.d/lankeeper.cfg <<'GRUBCFG'
GRUB_DISTRIBUTOR="LANKeeper __LANKEEPER_VERSION__"
GRUBCFG
if command -v update-grub >/dev/null 2>&1; then
    update-grub >/dev/null 2>&1 || true
fi

# SSH hardening — ensure PermitRootLogin yes / PasswordAuthentication yes
# regardless of whether the line is currently commented or not. The
# bootstrap nftables ruleset above already restricts SSH to the LAN
# subnet, and the agent's live ruleset takes over from there.
sed -i -E 's|^[[:space:]]*#?[[:space:]]*PermitRootLogin[[:space:]].*|PermitRootLogin yes|' /etc/ssh/sshd_config
sed -i -E 's|^[[:space:]]*#?[[:space:]]*PasswordAuthentication[[:space:]].*|PasswordAuthentication yes|' /etc/ssh/sshd_config
grep -q '^PermitRootLogin ' /etc/ssh/sshd_config || echo "PermitRootLogin yes" >> /etc/ssh/sshd_config
grep -q '^PasswordAuthentication ' /etc/ssh/sshd_config || echo "PasswordAuthentication yes" >> /etc/ssh/sshd_config

# Generate initial self-signed TLS certificate so the web service can start
# immediately on first boot. The dedicated `gen-cert` subcommand writes the
# cert synchronously and exits — no background process, no sleep race.
if [ ! -f "$DATA_DIR/tls/server.crt" ] && [ -f "$CONFIG_DIR/router.yaml" ]; then
    if "$INSTALL_DIR/$BINARY_NAME" gen-cert \
            --config "$CONFIG_DIR/router.yaml" \
            --data-dir "$DATA_DIR" >/dev/null; then
        chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR/tls" 2>/dev/null || true
        chmod 600 "$DATA_DIR/tls"/*.key 2>/dev/null || true
        chmod 644 "$DATA_DIR/tls"/*.crt 2>/dev/null || true
    else
        echo "HATA / ERROR: TLS sertifikası üretilemedi / TLS certificate generation failed" >&2
        exit 1
    fi
fi

# Tüm servis template'lerini /etc/ altına render et. Native Debian servisleri
# ilk boot'ta lankeeper config'leriyle başlasın.
if [ -f "$CONFIG_DIR/router.yaml" ] && [ -d "$DATA_DIR/configs/sysconf" ]; then
    if ! "$INSTALL_DIR/$BINARY_NAME" render-configs \
            --config "$CONFIG_DIR/router.yaml" \
            --cwd "$DATA_DIR"; then
        echo "HATA / ERROR: Servis config'leri render edilemedi / render-configs failed" >&2
        exit 1
    fi
fi

# Native Debian servislerini enable et — config'ler /etc/ altına yazıldı,
# ilk boot'ta lankeeper template'leriyle başlasınlar. dnsmasq Debian'da
# default disabled, açıkça enable ediyoruz. Diğerleri de idempotent.
for svc in unbound dnsmasq chrony rsyslog smbd nmbd; do
    systemctl enable "$svc" 2>/dev/null || true
done

echo "=== Kurulum tamamlandı / Post-install complete ==="
echo "Sistem LANKeeper olarak yeniden başlatılacak. / System will reboot into LANKeeper."
echo "Web arayüzü / Web UI: https://<LAN_IP>:8443"
