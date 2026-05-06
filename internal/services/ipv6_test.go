package services_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
)

const testDhcp6cConfTmpl = `interface {{ .WANInterface }} {
    send ia-pd 0;
{{- if .RapidCommit }}
    send rapid-commit;
{{- end }}
    request domain-name-servers;
    request domain-name;
    script "{{ .ScriptPath }}";
};
id-assoc pd 0 {
{{- range .PrefixInterfaces }}
    prefix-interface {{ .Device }} {
        sla-id {{ .SLAID }};
        sla-len {{ $.SLALen }};
    };
{{- end }}
};
`

const testDhcp6cScriptTmpl = `#!/bin/sh
STATE_FILE="{{ .StatePath }}"
echo "lease event"
`

const testDnsmasqRATmpl = `enable-ra
{{ range .Interfaces }}
{{- $iface := . -}}
interface={{ $iface.Device }}
dhcp-range=set:ra-{{ $iface.Device }},::,constructor:{{ $iface.Device }},ra-names,slaac,64,{{ $.LeaseTime }}
ra-param={{ $iface.Device }},mtu:{{ $.MTU }},{{ $.RAInterval }},0
{{- range $.RDNSSAddrs }}
dhcp-option=tag:ra-{{ $iface.Device }},option6:dns-server,[{{ . }}]
{{- end }}
{{- if $.SearchDomain }}
dhcp-option=tag:ra-{{ $iface.Device }},option6:domain-search,{{ $.SearchDomain }}
{{- end }}
{{ end }}
{{- if .ULAPrefix }}
dhcp-range={{ .ULAPrefix }},ra-only,64,{{ .LeaseTime }}
{{- end }}
`

func newIPv6TestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Interfaces = []config.InterfaceConfig{
		{ID: "wan", Device: "eth0", Role: "wan"},
		{ID: "lan", Device: "eth1", Role: "lan"},
	}
	cfg.PPPoE.Username = "user@isp"
	return cfg
}

func newIPv6TestService(t *testing.T, cfg *config.Config) *services.IPv6Service {
	t.Helper()
	return services.NewIPv6ServiceFromFS(cfg, testDhcp6cConfTmpl, testDhcp6cScriptTmpl, testDnsmasqRATmpl)
}

func TestNewIPv6Service(t *testing.T) {
	svc := services.NewIPv6Service(newIPv6TestConfig(t))
	if svc == nil {
		t.Fatal("service should not be nil")
	}
}

func TestIPv6RenderConfigPPPoE(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "interface ppp0") {
		t.Errorf("expected ppp0 (PPPoE WAN) in output, got:\n%s", out)
	}
	if !strings.Contains(out, "prefix-interface eth1") {
		t.Errorf("expected prefix-interface eth1 (LAN) in output, got:\n%s", out)
	}
	if !strings.Contains(out, "send rapid-commit") {
		t.Errorf("rapid-commit should be enabled by default, got:\n%s", out)
	}
	// /56 default delegation -> SLA len = 64-56 = 8.
	if !strings.Contains(out, "sla-len 8") {
		t.Errorf("expected sla-len 8 for /56, got:\n%s", out)
	}
}

func TestIPv6RenderConfigDirectWAN(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.PPPoE.Username = "" // simulate non-PPPoE WAN (DHCP/static)
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "interface eth0") {
		t.Errorf("expected eth0 (direct WAN) in output, got:\n%s", out)
	}
}

func TestIPv6RenderConfigCustomPrefixHint(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.WAN.PrefixHint = "/60"
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// /60 -> SLA len = 64-60 = 4.
	if !strings.Contains(out, "sla-len 4") {
		t.Errorf("expected sla-len 4 for /60, got:\n%s", out)
	}
}

func TestIPv6RenderConfigInvalidHint(t *testing.T) {
	cases := []string{"/40", "/72", "abc", "/-5"}
	for _, hint := range cases {
		cfg := newIPv6TestConfig(t)
		cfg.IPv6.WAN.PrefixHint = hint
		svc := newIPv6TestService(t, cfg)
		if _, err := svc.RenderConfig(); err == nil {
			t.Errorf("expected error for hint %q, got nil", hint)
		}
	}
}

func TestIPv6RenderConfigMissingInterfaces(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Interfaces = nil
	svc := newIPv6TestService(t, cfg)
	if _, err := svc.RenderConfig(); err == nil {
		t.Error("expected error when no interfaces configured")
	}
}

func TestIPv6RenderScriptContainsStatePath(t *testing.T) {
	svc := newIPv6TestService(t, newIPv6TestConfig(t))
	out, err := svc.RenderScript()
	if err != nil {
		t.Fatalf("render script: %v", err)
	}
	if !strings.Contains(out, "/var/lib/lankeeper/state/ipv6-prefix.json") {
		t.Errorf("expected state path in script, got:\n%s", out)
	}
	if !strings.Contains(out, "#!/bin/sh") {
		t.Errorf("script should start with #!/bin/sh shebang, got:\n%s", out)
	}
}

func TestPrefixStateActive(t *testing.T) {
	cases := []struct {
		name string
		ps   services.PrefixState
		want bool
	}{
		{"empty", services.PrefixState{}, false},
		{"valid", services.PrefixState{Prefix: "2001:db8::", PrefixLength: 56, ValidLifetime: 3600, Reason: "REPLY"}, true},
		{"released", services.PrefixState{Prefix: "2001:db8::", PrefixLength: 56, ValidLifetime: 3600, Reason: "RELEASE"}, false},
		{"exit", services.PrefixState{Prefix: "2001:db8::", PrefixLength: 56, ValidLifetime: 3600, Reason: "EXIT"}, false},
		{"expired", services.PrefixState{Prefix: "2001:db8::", PrefixLength: 56, ValidLifetime: 0, Reason: "REPLY"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ps.Active(); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrefixStateCIDR(t *testing.T) {
	ps := services.PrefixState{Prefix: "2001:db8::", PrefixLength: 56}
	if got := ps.CIDR(); got != "2001:db8::/56" {
		t.Errorf("CIDR() = %q, want 2001:db8::/56", got)
	}

	empty := services.PrefixState{}
	if got := empty.CIDR(); got != "" {
		t.Errorf("empty CIDR() = %q, want empty string", got)
	}
}

func TestIPv6RenderConfigVLANsGetSubPrefixes(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 13, Role: "guest"},
		{ID: "iot", Parent: "lan", VID: 20, Role: "iot"},
	}
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Expected: lan -> sla-id 0, guest (eth1.13) -> 1, iot (eth1.20) -> 2.
	for _, want := range []string{
		"prefix-interface eth1 ",
		"prefix-interface eth1.13 ",
		"prefix-interface eth1.20 ",
		"sla-id 0",
		"sla-id 1",
		"sla-id 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in rendered config, got:\n%s", want, out)
		}
	}
}

func TestIPv6RenderConfigSubnetMapOverride(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 13, Role: "guest"},
	}
	cfg.IPv6.LAN.SubnetMap = map[string]int{
		"lan":   2,
		"guest": 7,
	}
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Both overrides must be honoured verbatim.
	if !strings.Contains(out, "sla-id 2") {
		t.Errorf("expected sla-id 2 (lan override), got:\n%s", out)
	}
	if !strings.Contains(out, "sla-id 7") {
		t.Errorf("expected sla-id 7 (guest override), got:\n%s", out)
	}
}

func TestIPv6RenderConfigSlash64NoVLANs(t *testing.T) {
	// /64 delegation -> sla-len 0 -> no room for VLAN sub-prefixes.
	// Render must still emit the primary LAN block but skip VLANs.
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.WAN.PrefixHint = "/64"
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 13, Role: "guest"},
	}
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "prefix-interface eth1 ") {
		t.Errorf("expected primary LAN entry, got:\n%s", out)
	}
	if strings.Contains(out, "eth1.13") {
		t.Errorf("VLAN entry must be skipped on /64 delegation, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigLANOnly(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if !strings.Contains(out, "enable-ra") {
		t.Errorf("expected enable-ra directive, got:\n%s", out)
	}
	if !strings.Contains(out, "interface=eth1") {
		t.Errorf("expected interface=eth1 (primary LAN), got:\n%s", out)
	}
	if !strings.Contains(out, "constructor:eth1") {
		t.Errorf("expected constructor:eth1, got:\n%s", out)
	}
	if !strings.Contains(out, "ra-names,slaac,64") {
		t.Errorf("expected ra-names,slaac,64 mode, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigVLANs(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 13, Role: "guest"},
		{ID: "iot", Parent: "lan", VID: 20, Role: "iot"},
	}
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	for _, want := range []string{
		"interface=eth1\n",
		"interface=eth1.13\n",
		"interface=eth1.20\n",
		"constructor:eth1.13",
		"constructor:eth1.20",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in RA config, got:\n%s", want, out)
		}
	}
}

func TestIPv6RenderRAConfigSlash64SkipsVLANs(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.WAN.PrefixHint = "/64"
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 13, Role: "guest"},
	}
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if !strings.Contains(out, "interface=eth1\n") {
		t.Errorf("expected primary LAN interface, got:\n%s", out)
	}
	if strings.Contains(out, "eth1.13") {
		t.Errorf("VLAN must be skipped on /64 delegation, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigDisabled(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.Enabled = "off"
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty RA config when IPv6 disabled, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigCustomInterval(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.LAN.RAInterval = 60
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if !strings.Contains(out, ",60,0") {
		t.Errorf("expected custom RA interval 60, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigULA(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.LAN.ULA.Enabled = true
	cfg.IPv6.LAN.ULA.Prefix = "fd00:abcd:1234::/48"
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if !strings.Contains(out, "fd00:abcd:1234::/48") {
		t.Errorf("expected ULA prefix in RA config, got:\n%s", out)
	}
}

func TestIPv6AnnouncedInterfacesLANOnly(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	got, err := svc.AnnouncedInterfaces()
	if err != nil {
		t.Fatalf("announced: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(got), got)
	}
	if got[0].Device != "eth1" || got[0].SLAID != 0 {
		t.Errorf("expected eth1/0, got %+v", got[0])
	}
}

func TestIPv6AnnouncedInterfacesWithVLANs(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.VLANs = []config.VLANConfig{
		{ID: "guest", Parent: "lan", VID: 13, Role: "guest"},
		{ID: "iot", Parent: "lan", VID: 20, Role: "iot"},
	}
	svc := newIPv6TestService(t, cfg)

	got, err := svc.AnnouncedInterfaces()
	if err != nil {
		t.Fatalf("announced: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected LAN + 2 VLANs = 3 entries, got %d: %+v", len(got), got)
	}
	wantDevices := []string{"eth1", "eth1.13", "eth1.20"}
	for i, want := range wantDevices {
		if got[i].Device != want {
			t.Errorf("entry %d: expected device %q, got %q", i, want, got[i].Device)
		}
	}
}

func TestIPv6AnnouncedInterfacesDisabled(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.Enabled = "off"
	svc := newIPv6TestService(t, cfg)

	got, err := svc.AnnouncedInterfaces()
	if err != nil {
		t.Fatalf("announced: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when disabled, got %+v", got)
	}
}

func TestIPv6SetOnLeaseChangeIsRegistered(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	// Just exercising the setter — it must accept a nil and a real
	// callback without panicking, so subsequent dispatch is safe.
	svc.SetOnLeaseChange(nil)
	svc.SetOnLeaseChange(func(_ context.Context, _ services.PrefixState) error {
		return nil
	})
}

func TestIPv6StopLeaseWatcherWithoutStartIsNoop(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	// Calling Stop before Start (or twice) must not deadlock or panic.
	svc.StopLeaseWatcher()
	svc.StopLeaseWatcher()
}

func TestIPv6LeaseWatcherFiresOnFileChange(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	statePath := filepath.Join(t.TempDir(), "ipv6-prefix.json")
	svc.SetStatePathForTest(statePath)

	calls := make(chan services.PrefixState, 8)
	svc.SetOnLeaseChange(func(_ context.Context, st services.PrefixState) error {
		calls <- st
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.StartLeaseWatcher(ctx); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	defer svc.StopLeaseWatcher()

	// Initial dispatch fires once with the empty state.
	select {
	case st := <-calls:
		if st.Active() {
			t.Fatalf("expected inactive initial state, got %+v", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial dispatch never fired")
	}

	// Simulate the dhcp6c hook script's atomic-mv write. Use the
	// current wall-clock timestamp so PrefixState.Active() does not
	// short-circuit on Expired() (lease lifetime 7200s starts now).
	tmp := statePath + ".tmp"
	body := []byte(fmt.Sprintf(`{"timestamp":%d,"reason":"REPLY","prefix":"2001:db8::","prefixLength":56,"preferredLifetime":3600,"validLifetime":7200}`, time.Now().Unix()))
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, statePath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	select {
	case st := <-calls:
		if !st.Active() {
			t.Fatalf("expected Active state after lease write, got %+v", st)
		}
		if st.Prefix != "2001:db8::" || st.PrefixLength != 56 {
			t.Errorf("unexpected lease body: %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lease change dispatch never fired")
	}
}

func TestIPv6LeaseWatcherDedupesIdenticalEvents(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	svc := newIPv6TestService(t, cfg)

	statePath := filepath.Join(t.TempDir(), "ipv6-prefix.json")
	svc.SetStatePathForTest(statePath)

	// Pre-write the lease so the initial dispatch consumes one slot.
	body := []byte(`{"timestamp":1,"reason":"REPLY","prefix":"2001:db8::","prefixLength":56,"preferredLifetime":3600,"validLifetime":7200}`)
	if err := os.WriteFile(statePath, body, 0o644); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	calls := make(chan struct{}, 8)
	svc.SetOnLeaseChange(func(_ context.Context, _ services.PrefixState) error {
		calls <- struct{}{}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.StartLeaseWatcher(ctx); err != nil {
		t.Fatalf("StartLeaseWatcher: %v", err)
	}
	defer svc.StopLeaseWatcher()

	// Initial dispatch.
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("initial dispatch never fired")
	}

	// Re-write the same body — same hash, callback must NOT fire.
	if err := os.WriteFile(statePath, body, 0o644); err != nil {
		t.Fatalf("rewrite lease: %v", err)
	}

	select {
	case <-calls:
		t.Fatal("callback fired for identical lease (dedup broken)")
	case <-time.After(500 * time.Millisecond):
		// expected silence
	}
}

func TestPrefixStateExpiresIn(t *testing.T) {
	now := time.Now().Unix()

	expired := services.PrefixState{
		Timestamp: now - 7200, ValidLifetime: 3600,
		Prefix: "2001:db8::", PrefixLength: 56, Reason: "REPLY",
	}
	if !expired.Expired() {
		t.Errorf("Expired() should be true past validLifetime")
	}
	if expired.Active() {
		t.Errorf("Active() must be false once expired even with REPLY reason")
	}
	if d := expired.ExpiresIn(); d >= 0 {
		t.Errorf("ExpiresIn() should be negative for expired lease, got %v", d)
	}

	fresh := services.PrefixState{
		Timestamp: now, ValidLifetime: 3600,
		Prefix: "2001:db8::", PrefixLength: 56, Reason: "REPLY",
	}
	if fresh.Expired() {
		t.Errorf("Expired() should be false for fresh lease")
	}
	if !fresh.Active() {
		t.Errorf("Active() should be true for fresh lease")
	}
	d := fresh.ExpiresIn()
	if d <= 0 || d > time.Hour {
		t.Errorf("ExpiresIn() should be ~1h, got %v", d)
	}

	zero := services.PrefixState{}
	if zero.Expired() {
		t.Errorf("Expired() should be false for zero PrefixState")
	}
	if zero.ExpiresIn() != 0 {
		t.Errorf("ExpiresIn() should be 0 for zero PrefixState, got %v", zero.ExpiresIn())
	}
}

func TestIPv6RenderRAConfigEmbedsMTUAndDomain(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.System.Domain = "hermes.lan"
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if !strings.Contains(out, "mtu:1492") {
		t.Errorf("expected PPPoE MTU 1492 in ra-param, got:\n%s", out)
	}
	if !strings.Contains(out, "option6:domain-search,hermes.lan") {
		t.Errorf("expected DNSSL option for hermes.lan, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigDirectWANUsesMTU1500(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.PPPoE.Username = "" // direct WAN, not PPPoE
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("render RA: %v", err)
	}
	if !strings.Contains(out, "mtu:1500") {
		t.Errorf("expected MTU 1500 for non-PPPoE WAN, got:\n%s", out)
	}
}

func TestIPv6RenderRAConfigSixInFourUsesRoutedPrefix(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.Mode = "6in4"
	cfg.IPv6.WAN.RequestPrefix = false
	cfg.IPv6.Tunnel.RoutedPrefix = "2001:470:abcd::/48"
	cfg.IPv6.Tunnel.ServerIPv4 = "216.66.80.30"
	cfg.IPv6.Tunnel.ClientIPv6 = "2001:470:1f0a:abc::2"
	svc := newIPv6TestService(t, cfg)

	out, err := svc.RenderRAConfig()
	if err != nil {
		t.Fatalf("RenderRAConfig: %v", err)
	}
	// In 6in4 mode the advertised MTU is the tunnel MTU (1452 over
	// PPPoE) rather than the link MTU (1492).
	if !strings.Contains(out, "mtu:1452") {
		t.Errorf("expected mtu:1452 for 6in4 + PPPoE, got:\n%s", out)
	}
	// /48 routed prefix must yield 64-48=16 bits of SLA per /64;
	// the LAN bridge sub-prefix should land on sla-id 0.
	if !strings.Contains(out, "interface=eth1") {
		t.Errorf("LAN bridge missing from RA output:\n%s", out)
	}
}

func TestGenerateULAPrefixIsValid(t *testing.T) {
	// Cover the helper directly via repeated calls — should never
	// return the same prefix twice (40 random bits) and must always
	// match the fdXX:XXXX:XXXX::/48 shape.
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		p, err := services.GenerateULAPrefixForTest()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if !strings.HasPrefix(p, "fd") {
			t.Errorf("ULA must start with fd, got %q", p)
		}
		if !strings.HasSuffix(p, "::/48") {
			t.Errorf("ULA must end with ::/48, got %q", p)
		}
		if seen[p] {
			t.Errorf("duplicate ULA generated: %q", p)
		}
		seen[p] = true
	}
}

func TestIPv6IsDisabledRendersStub(t *testing.T) {
	cfg := newIPv6TestConfig(t)
	cfg.IPv6.Enabled = "off"
	svc := newIPv6TestService(t, cfg)

	// RenderToDisk needs file I/O which calls into netutil; we cannot
	// run that fully in unit tests. RenderConfig is the testable part:
	// when Enabled is "off" callers should simply skip rendering. The
	// RenderConfig stays usable so the caller decides.
	out, err := svc.RenderConfig()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out == "" {
		t.Error("RenderConfig should return content even when disabled")
	}
}
