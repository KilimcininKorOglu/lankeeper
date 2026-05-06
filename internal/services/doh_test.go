package services

import (
	"strings"
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

func TestValidateDoHUpstream(t *testing.T) {
	svc := &DoHService{cfg: &config.Config{}}

	cases := []struct {
		name    string
		spec    string
		wantErr bool
		errSub  string
	}{
		{"empty", "", true, "required"},
		{"catalogue cloudflare", "cloudflare", false, ""},
		{"https valid", "https://1.1.1.1/dns-query", false, ""},
		{"https with hostname", "https://dns.cloudflare.com/dns-query", false, ""},
		{"https no path", "https://1.1.1.1/", true, "DoH endpoint"},
		{"https bad scheme", "http://1.1.1.1/dns-query", true, "https"},
		{"https bad port", "https://1.1.1.1:9999/dns-query", true, "port"},
		{"https internal IP", "https://192.168.1.1/dns-query", true, "internal"},
		{"https loopback", "https://127.0.0.1/dns-query", true, "internal"},
		{"sdns invalid b64", "sdns://!!!", true, "base64"},
		{"plain string", "not-a-thing", true, "catalogue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.ValidateUpstream(tc.spec)
			if tc.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if tc.wantErr && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errSub)) {
				t.Errorf("error %q missing substring %q", err, tc.errSub)
			}
		})
	}
}

func TestHTTPSURLToStampRoundTrip(t *testing.T) {
	stamp, err := httpsURLToStamp("https://1.1.1.1/dns-query")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stamp, "sdns://") {
		t.Errorf("stamp %q missing prefix", stamp)
	}
	// Decode and re-validate the round-trip.
	if err := validateSDNSUpstream(stamp); err != nil {
		t.Errorf("stamp re-validation: %v", err)
	}
}

func TestRenderConfigCatalogue(t *testing.T) {
	cfg := &config.Config{}
	cfg.DNS.EnableDoH = true
	cfg.DNS.DoHUpstream = "cloudflare"
	svc, err := NewDoHServiceFromFS(cfg, testDoHTemplate)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.RenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "server_names = ['cloudflare']") {
		t.Errorf("rendered config missing cloudflare server_names: %s", got)
	}
	if strings.Contains(got, "[static]") {
		t.Errorf("catalogue pick should not emit [static] block")
	}
}

func TestRenderConfigCustomURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.DNS.EnableDoH = true
	cfg.DNS.DoHUpstream = "https://dns.example.com/dns-query"
	svc, err := NewDoHServiceFromFS(cfg, testDoHTemplate)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.RenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "lankeeper-custom") {
		t.Errorf("custom URL should produce lankeeper-custom static entry: %s", got)
	}
	if !strings.Contains(got, "[static]") {
		t.Errorf("custom URL should emit [static] block: %s", got)
	}
}

func TestValidateDoHPort(t *testing.T) {
	for _, p := range []string{"443", "4443", "8443"} {
		if err := validateDoHPort(p); err != nil {
			t.Errorf("port %s rejected: %v", p, err)
		}
	}
	for _, p := range []string{"80", "53", "0", "9999"} {
		if err := validateDoHPort(p); err == nil {
			t.Errorf("port %s should be rejected", p)
		}
	}
}

// testDoHTemplate mirrors configs/sysconf/dnscrypt-proxy.toml.tmpl
// for tests so they don't depend on the working directory or the
// embedded filesystem layout.
const testDoHTemplate = `listen_addresses = ['127.0.0.1:5353']
{{- if .ServerNames }}
server_names = [{{ range $i, $n := .ServerNames }}{{ if $i }}, {{ end }}'{{ $n }}'{{ end }}]
{{- end }}
{{- if .CustomServers }}

[static]
{{- range .CustomServers }}
  [static.'{{ .Name }}']
  stamp = '{{ .Stamp }}'
{{- end }}
{{- end }}
`
