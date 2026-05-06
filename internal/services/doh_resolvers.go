package services

// BuiltInDoHResolver is one entry in the curated dnscrypt-proxy
// resolver catalogue. The Name matches a server in the public
// public-resolvers.md list (refreshed by dnscrypt-proxy itself);
// Description is shown in the UI dropdown.
//
// We deliberately curate a small list rather than dumping all ~280
// public servers - operators want the obvious major-provider names
// for picking, and they can always set DoHUpstream to a custom
// `https://...` or `sdns://` spec if they need something exotic.
type BuiltInDoHResolver struct {
	Name        string
	Description string
}

// BuiltInDoHResolvers returns the curated catalogue. List order is
// the dropdown order in the UI: privacy-focused providers first,
// then geographic/no-filter alternatives. Names come from the
// dnscrypt.info public-resolvers.md v3 list and must stay in sync
// when that list is regenerated; if upstream renames a server we
// just drop ours here (the operator can switch via custom URL).
func BuiltInDoHResolvers() []BuiltInDoHResolver {
	return []BuiltInDoHResolver{
		{
			Name:        "cloudflare",
			Description: "Cloudflare 1.1.1.1 (no logging, no filter)",
		},
		{
			Name:        "cloudflare-security",
			Description: "Cloudflare 1.1.1.2 (malware filter)",
		},
		{
			Name:        "cloudflare-family",
			Description: "Cloudflare 1.1.1.3 (malware + adult filter)",
		},
		{
			Name:        "quad9-doh-ip4-port443-nofilter-pri",
			Description: "Quad9 9.9.9.10 (no filter, no DNSSEC, no ECS)",
		},
		{
			Name:        "quad9-doh-ip4-port443-filter-pri",
			Description: "Quad9 9.9.9.9 (malware filter, DNSSEC validated)",
		},
		{
			Name:        "google",
			Description: "Google 8.8.8.8 (no logging policy)",
		},
		{
			Name:        "adguard-dns-doh",
			Description: "AdGuard 94.140.14.14 (default ad-block)",
		},
		{
			Name:        "adguard-dns-unfiltered-doh",
			Description: "AdGuard 94.140.14.140 (no filter)",
		},
		{
			Name:        "nextdns",
			Description: "NextDNS (configure profile via custom sdns://)",
		},
		{
			Name:        "mullvad-doh",
			Description: "Mullvad (no logging, Sweden)",
		},
	}
}

// IsBuiltInDoHResolver reports whether name matches one of the
// curated entries. Used by the form handler to discriminate
// between catalogue picks and operator-supplied custom URLs.
func IsBuiltInDoHResolver(name string) bool {
	for _, r := range BuiltInDoHResolvers() {
		if r.Name == name {
			return true
		}
	}
	return false
}
