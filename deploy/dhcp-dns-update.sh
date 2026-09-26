#!/usr/bin/env bash
# Called by dnsmasq dhcp-script on lease events
# Arguments: $1=action (add|old|del), $2=MAC, $3=IP, $4=hostname

ACTION="$1"
MAC="$2"
IP="$3"
HOSTNAME="$4"
DOMAIN="lan"
CONFIG="${LANKEEPER_CONFIG:-/etc/lankeeper/router.yaml}"
UNBOUND_CONF="${LANKEEPER_UNBOUND_CONF:-/etc/unbound/unbound.conf}"

if [[ -f "$CONFIG" ]]; then
    CONFIGURED_DOMAIN=$(grep -oP 'domain:\s*"\K[^"]+' "$CONFIG" 2>/dev/null)
    if [[ -n "$CONFIGURED_DOMAIN" ]]; then
        DOMAIN="$CONFIGURED_DOMAIN"
    fi
fi

if [[ -z "$HOSTNAME" || "$HOSTNAME" == "*" ]]; then
    exit 0
fi

FQDN="${HOSTNAME}.${DOMAIN}"

# The hostname is chosen by the client, so it has no authority over names
# the operator owns: a static record, a reserved lease's mirror (both are
# local-data lines in the Unbound config) or the router's own name.
# Publishing or removing one of those would let any client on any segment
# take over or delete it.
operator_owned() {
    local router
    router=$(awk '$1 == "hostname:" { gsub(/"/, "", $2); print $2; exit }' "$CONFIG" 2>/dev/null)
    if [[ -n "$router" && "${HOSTNAME,,}" == "${router,,}" ]]; then
        return 0
    fi
    grep -qiF -e "local-data: \"$FQDN. " -e "local-data: \"$HOSTNAME. " "$UNBOUND_CONF" 2>/dev/null
}

# points_at reports whether Unbound currently answers name with ip, so a
# release removes only the record this lease published.
points_at() {
    unbound-control list_local_data 2>/dev/null |
        awk -v n="${1,,}" -v ip="$2" 'tolower($1) == n && $4 == "A" && $5 == ip { f = 1 } END { exit !f }'
}

if operator_owned; then
    exit 0
fi

case "$ACTION" in
    add|old)
        unbound-control local_data "$FQDN. 300 IN A $IP" 2>/dev/null
        unbound-control local_data_remove "$HOSTNAME." 2>/dev/null
        unbound-control local_data "$HOSTNAME. 300 IN A $IP" 2>/dev/null

        PTR=$(echo "$IP" | awk -F. '{print $4"."$3"."$2"."$1".in-addr.arpa."}')
        unbound-control local_data "$PTR 300 IN PTR $FQDN." 2>/dev/null
        ;;
    del)
        if points_at "$FQDN." "$IP"; then
            unbound-control local_data_remove "$FQDN." 2>/dev/null
        fi
        if points_at "$HOSTNAME." "$IP"; then
            unbound-control local_data_remove "$HOSTNAME." 2>/dev/null
        fi

        PTR=$(echo "$IP" | awk -F. '{print $4"."$3"."$2"."$1".in-addr.arpa."}')
        unbound-control local_data_remove "$PTR" 2>/dev/null
        ;;
esac
