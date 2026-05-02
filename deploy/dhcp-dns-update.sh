#!/usr/bin/env bash
# Called by dnsmasq dhcp-script on lease events
# Arguments: $1=action (add|old|del), $2=MAC, $3=IP, $4=hostname

ACTION="$1"
MAC="$2"
IP="$3"
HOSTNAME="$4"
DOMAIN="lan"

if [[ -f /etc/home-router/router.yaml ]]; then
    CONFIGURED_DOMAIN=$(grep -oP 'domain:\s*"\K[^"]+' /etc/home-router/router.yaml 2>/dev/null)
    if [[ -n "$CONFIGURED_DOMAIN" ]]; then
        DOMAIN="$CONFIGURED_DOMAIN"
    fi
fi

if [[ -z "$HOSTNAME" || "$HOSTNAME" == "*" ]]; then
    exit 0
fi

FQDN="${HOSTNAME}.${DOMAIN}"

case "$ACTION" in
    add|old)
        unbound-control local_data "$FQDN. 300 IN A $IP" 2>/dev/null
        unbound-control local_data_remove "$HOSTNAME." 2>/dev/null
        unbound-control local_data "$HOSTNAME. 300 IN A $IP" 2>/dev/null

        PTR=$(echo "$IP" | awk -F. '{print $4"."$3"."$2"."$1".in-addr.arpa."}')
        unbound-control local_data "$PTR 300 IN PTR $FQDN." 2>/dev/null
        ;;
    del)
        unbound-control local_data_remove "$FQDN." 2>/dev/null
        unbound-control local_data_remove "$HOSTNAME." 2>/dev/null

        PTR=$(echo "$IP" | awk -F. '{print $4"."$3"."$2"."$1".in-addr.arpa."}')
        unbound-control local_data_remove "$PTR" 2>/dev/null
        ;;
esac
