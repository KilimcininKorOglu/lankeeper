package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type RoutingService struct {
	cfg            *config.Config
	mu             sync.RWMutex
	domainSets     map[string]map[string]bool
	domainCancel   context.CancelFunc
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
			s.setupDomainSet(ctx, p.Name, p.Domains, tunnel.Fwmark)
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

	sb.WriteString("flush chain inet filter pbr_policies 2>/dev/null\n")
	sb.WriteString("add chain inet filter pbr_policies { type filter hook forward priority -1 ; policy accept ; }\n")

	for _, p := range policies {
		if !p.Enabled {
			continue
		}

		tunnel := s.findTunnel(p.Tunnel)
		if tunnel == nil {
			continue
		}

		schedulePrefix := ""
		if p.Schedule != "" {
			schedulePrefix = buildScheduleMatch(p.Schedule)
		}

		for _, mac := range p.SrcMACs {
			fmt.Fprintf(&sb, "add rule inet filter pbr_policies %sether saddr %s meta mark set %d\n",
				schedulePrefix, mac, tunnel.Fwmark)
		}
		for _, ip := range p.SrcIPs {
			fmt.Fprintf(&sb, "add rule inet filter pbr_policies %sip saddr %s meta mark set %d\n",
				schedulePrefix, ip, tunnel.Fwmark)
		}
		for _, dst := range p.DstIPs {
			fmt.Fprintf(&sb, "add rule inet filter pbr_policies %sip daddr %s meta mark set %d\n",
				schedulePrefix, dst, tunnel.Fwmark)
		}
		for _, port := range p.DstPorts {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			fmt.Fprintf(&sb, "add rule inet filter pbr_policies %s%s dport %d meta mark set %d\n",
				schedulePrefix, proto, port, tunnel.Fwmark)
		}

		if len(p.Domains) > 0 {
			setName := "pbr_" + sanitizeName(p.Name)
			fmt.Fprintf(&sb, "add set inet filter %s { type ipv4_addr ; flags timeout ; }\n", setName)
			fmt.Fprintf(&sb, "add rule inet filter pbr_policies %sip daddr @%s meta mark set %d\n",
				schedulePrefix, setName, tunnel.Fwmark)
		}

		if p.KillSwitch {
			for _, mac := range p.SrcMACs {
				fmt.Fprintf(&sb, "add rule inet filter pbr_policies ether saddr %s meta mark != %d drop\n",
					mac, tunnel.Fwmark)
			}
			for _, ip := range p.SrcIPs {
				fmt.Fprintf(&sb, "add rule inet filter pbr_policies ip saddr %s meta mark != %d drop\n",
					ip, tunnel.Fwmark)
			}
		}
	}

	sb.WriteString("add rule inet filter pbr_policies ct mark set meta mark\n")

	return sb.String()
}

func (s *RoutingService) applyNftRules(ctx context.Context, rules string) error {
	tmpFile := "/tmp/pbr-rules.nft"
	if err := os.WriteFile(tmpFile, []byte(rules), 0o600); err != nil {
		return fmt.Errorf("write PBR rules: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile) }()

	_, err := netutil.Run(ctx, "nft", "-f", tmpFile)
	return err
}

func (s *RoutingService) setupDomainSet(ctx context.Context, policyName string, domains []string, fwmark int) {
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
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
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
