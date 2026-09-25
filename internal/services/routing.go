package services

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type RoutingService struct {
	cfg          *config.Config
	mu           sync.RWMutex
	domainSets   map[string]map[string]bool
	domainCancel context.CancelFunc
}

func NewRoutingService(cfg *config.Config) *RoutingService {
	return &RoutingService{
		cfg:        cfg,
		domainSets: make(map[string]map[string]bool),
	}
}

func (s *RoutingService) GetPolicies() []config.RoutingPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()

	policies := make([]config.RoutingPolicy, len(s.cfg.Routing.Policies))
	copy(policies, s.cfg.Routing.Policies)

	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Priority < policies[j].Priority
	})

	return policies
}

func (s *RoutingService) persist() error {
	return s.cfg.SaveToFile()
}

func (s *RoutingService) AddPolicy(policy config.RoutingPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if policy.Priority == 0 {
		maxPrio := 0
		for _, p := range s.cfg.Routing.Policies {
			if p.Priority > maxPrio {
				maxPrio = p.Priority
			}
		}
		policy.Priority = maxPrio + 10
	}

	s.cfg.Routing.Policies = append(s.cfg.Routing.Policies, policy)
	return s.persist()
}

func (s *RoutingService) RemovePolicy(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, p := range s.cfg.Routing.Policies {
		if p.Name == name {
			s.cfg.Routing.Policies = append(s.cfg.Routing.Policies[:i], s.cfg.Routing.Policies[i+1:]...)
			return s.persist()
		}
	}
	return fmt.Errorf("policy %q not found", name)
}

func (s *RoutingService) UpdatePriorities(orderedNames []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	policyMap := make(map[string]*config.RoutingPolicy, len(s.cfg.Routing.Policies))
	for i := range s.cfg.Routing.Policies {
		policyMap[s.cfg.Routing.Policies[i].Name] = &s.cfg.Routing.Policies[i]
	}

	for i, name := range orderedNames {
		if p, ok := policyMap[name]; ok {
			p.Priority = (i + 1) * 10
		}
	}
	return s.persist()
}

func (s *RoutingService) TogglePolicy(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.cfg.Routing.Policies {
		if s.cfg.Routing.Policies[i].Name == name {
			s.cfg.Routing.Policies[i].Enabled = enabled
			return s.persist()
		}
	}
	return fmt.Errorf("policy %q not found", name)
}

func (s *RoutingService) Apply(ctx context.Context) error {
	s.mu.RLock()
	policies := make([]config.RoutingPolicy, len(s.cfg.Routing.Policies))
	copy(policies, s.cfg.Routing.Policies)
	s.mu.RUnlock()

	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Priority < policies[j].Priority
	})

	nftRules := s.generateFullNftChain(policies)
	if err := s.applyNftRules(ctx, nftRules); err != nil {
		return fmt.Errorf("apply nft PBR chain: %w", err)
	}

	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		tunnel := s.findTunnel(p.Tunnel)
		if tunnel == nil {
			continue
		}

		// Best-effort delete of the previous rule before adding the
		// fresh one; missing rules are not errors.
		_, _ = netutil.Run(ctx, "ip", "rule", "del", "fwmark",
			fmt.Sprintf("%d", tunnel.Fwmark), "lookup", fmt.Sprintf("%d", tunnel.Table))

		_, err := netutil.Run(ctx, "ip", "rule", "add", "fwmark",
			fmt.Sprintf("%d", tunnel.Fwmark), "lookup", fmt.Sprintf("%d", tunnel.Table),
			"priority", fmt.Sprintf("%d", p.Priority))
		if err != nil {
			log.Printf("ip rule add for policy %q: %v", p.Name, err)
		}

		if len(p.Domains) > 0 {
			s.setupDomainSet(ctx, p.Name, p.Domains)
		}
	}

	log.Printf("PBR applied: %d policies", len(policies))
	return nil
}

func (s *RoutingService) Clear(ctx context.Context) error {
	// All deletes below are best-effort cleanup; missing objects are
	// not errors. The function never fails — at worst we leave stale
	// state that the next Apply will overwrite.
	_, _ = netutil.Run(ctx, "nft", "delete", "chain", "inet", "filter", "pbr_policies")

	for _, p := range s.cfg.Routing.Policies {
		tunnel := s.findTunnel(p.Tunnel)
		if tunnel == nil {
			continue
		}
		_, _ = netutil.Run(ctx, "ip", "rule", "del", "fwmark",
			fmt.Sprintf("%d", tunnel.Fwmark), "lookup", fmt.Sprintf("%d", tunnel.Table))

		if len(p.Domains) > 0 {
			_, _ = netutil.Run(ctx, "nft", "delete", "set", "inet", "filter", "pbr_"+sanitizeName(p.Name))
		}
	}

	if s.domainCancel != nil {
		s.domainCancel()
	}

	return nil
}

func (s *RoutingService) generateFullNftChain(policies []config.RoutingPolicy) string {
	var sb strings.Builder

	// add is a no-op on an existing chain, so the flush after it always
	// has a chain to empty and a re-apply does not duplicate rules. The
	// script is read by nft, not a shell, so no redirection may appear.
	sb.WriteString("add chain inet filter pbr_policies { type filter hook forward priority -1 ; policy accept ; }\n")
	sb.WriteString("flush chain inet filter pbr_policies\n")

	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		if tunnel := s.findTunnel(p.Tunnel); tunnel != nil {
			writePolicyRules(&sb, p, tunnel.Fwmark)
		}
	}

	sb.WriteString("add rule inet filter pbr_policies ct mark set meta mark\n")

	return sb.String()
}

// pbrTmpPath is the scratch file nft loads the PBR chain from. The agent
// writes it, because the agent and the web process each run with
// PrivateTmp: a file the web process writes to its own /tmp is absent
// from the /tmp the agent's nft reads.
const pbrTmpPath = "/tmp/lankeeper-pbr.nft"

// writePolicyRules writes the mark rules for one policy: its source
// MACs and IPs, destination IPs and ports, its domain set, and the
// kill-switch drops.
func writePolicyRules(sb *strings.Builder, p config.RoutingPolicy, fwmark int) {
	schedulePrefix := ""
	if p.Schedule != "" {
		schedulePrefix = buildScheduleMatch(p.Schedule)
	}
	mark := func(match string) {
		fmt.Fprintf(sb, "add rule inet filter pbr_policies %s%s meta mark set %d\n", schedulePrefix, match, fwmark)
	}

	for _, mac := range p.SrcMACs {
		mark("ether saddr " + mac)
	}
	for _, ip := range p.SrcIPs {
		mark("ip saddr " + ip)
	}
	for _, dst := range p.DstIPs {
		mark("ip daddr " + dst)
	}
	proto := cmp.Or(p.Protocol, "tcp")
	for _, port := range p.DstPorts {
		mark(fmt.Sprintf("%s dport %d", proto, port))
	}

	if len(p.Domains) > 0 {
		setName := "pbr_" + sanitizeName(p.Name)
		fmt.Fprintf(sb, "add set inet filter %s { type ipv4_addr ; flags timeout ; }\n", setName)
		mark("ip daddr @" + setName)
	}

	if !p.KillSwitch {
		return
	}
	for _, mac := range p.SrcMACs {
		fmt.Fprintf(sb, "add rule inet filter pbr_policies ether saddr %s meta mark != %d drop\n", mac, fwmark)
	}
	for _, ip := range p.SrcIPs {
		fmt.Fprintf(sb, "add rule inet filter pbr_policies ip saddr %s meta mark != %d drop\n", ip, fwmark)
	}
}

func (s *RoutingService) applyNftRules(ctx context.Context, rules string) error {
	if err := netutil.WriteFile(pbrTmpPath, []byte(rules), 0o600); err != nil {
		return fmt.Errorf("write PBR rules: %w", err)
	}
	_, err := netutil.Run(ctx, "nft", "-f", pbrTmpPath)
	return err
}

func (s *RoutingService) setupDomainSet(ctx context.Context, policyName string, domains []string) {
	setName := "pbr_" + sanitizeName(policyName)

	s.mu.Lock()
	if s.domainSets[setName] == nil {
		s.domainSets[setName] = make(map[string]bool)
	}
	for _, d := range domains {
		s.domainSets[setName][d] = true
	}
	s.mu.Unlock()

	s.resolveDomains(ctx, setName, domains)
}

func (s *RoutingService) resolveDomains(ctx context.Context, setName string, domains []string) {
	for _, domain := range domains {
		out, err := netutil.RunSimple(ctx, "dig", "+short", domain)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
			ip := strings.TrimSpace(line)
			if ip == "" || strings.Contains(ip, ":") {
				continue
			}
			// Best-effort: missing set is logged at apply time. Per-IP
			// add failures are tolerated (next refresh will retry).
			_, _ = netutil.Run(ctx, "nft", "add", "element", "inet", "filter", setName,
				"{", ip, "timeout", "300s", "}")
		}
	}
}

func (s *RoutingService) StartDomainRefresh(ctx context.Context) {
	ctx, s.domainCancel = context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.RLock()
				for setName, domainMap := range s.domainSets {
					var domains []string
					for d := range domainMap {
						domains = append(domains, d)
					}
					s.resolveDomains(ctx, setName, domains)
				}
				s.mu.RUnlock()
			}
		}
	}()
}

func buildScheduleMatch(schedule string) string {
	parts := strings.Split(schedule, "-")
	if len(parts) != 2 {
		return ""
	}

	start := strings.TrimSpace(parts[0])
	end := strings.TrimSpace(parts[1])

	if start == "" || end == "" {
		return ""
	}

	return fmt.Sprintf("meta hour >= \"%s\" meta hour < \"%s\" ", start, end)
}

func (s *RoutingService) GenerateNftRules() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	policies := make([]config.RoutingPolicy, len(s.cfg.Routing.Policies))
	copy(policies, s.cfg.Routing.Policies)

	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Priority < policies[j].Priority
	})

	return s.generateFullNftChain(policies)
}

type tunnelRef struct {
	Table  int
	Fwmark int
}

func (s *RoutingService) findTunnel(name string) *tunnelRef {
	for _, t := range s.cfg.VPN.Clients {
		if t.Name == name {
			return &tunnelRef{Table: t.Table, Fwmark: t.Fwmark}
		}
	}
	for _, t := range s.cfg.OpenVPN.Clients {
		if t.Name == name {
			return &tunnelRef{Table: t.Table, Fwmark: t.Fwmark}
		}
	}
	return nil
}

func sanitizeName(s string) string {
	r := strings.NewReplacer(" ", "_", "-", "_", ".", "_")
	return r.Replace(s)
}
