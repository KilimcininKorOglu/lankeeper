package services

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type HealthCheckService struct {
	cfg     *config.Config
	mu      sync.RWMutex
	results map[string]*CheckResult
	cancel  context.CancelFunc
}

type CheckResult struct {
	Name         string
	Status       string
	LastCheck    time.Time
	FailureCount int
	LastAction   string
	LastActionAt time.Time
	InCooldown   bool
}

func NewHealthCheckService(cfg *config.Config) *HealthCheckService {
	return &HealthCheckService{
		cfg:     cfg,
		results: make(map[string]*CheckResult),
	}
}

// Start seeds the result map and spawns one goroutine per configured
// check. Remediation actions bring interfaces down and restart pppd, so
// the enabled flag is enforced here rather than at the call site: the
// service owns the config section, and a future caller cannot skip the
// gate by forgetting to test it.
func (s *HealthCheckService) Start(ctx context.Context) {
	if !s.cfg.HealthCheck.Enabled {
		log.Print("health check disabled by configuration")
		return
	}

	ctx, s.cancel = context.WithCancel(ctx)

	for _, check := range s.cfg.HealthCheck.Checks {
		s.mu.Lock()
		s.results[check.Name] = &CheckResult{
			Name:   check.Name,
			Status: "unknown",
		}
		s.mu.Unlock()

		go s.runCheck(ctx, check)
	}

	log.Printf("health check started (%d checks)", len(s.cfg.HealthCheck.Checks))
}

func (s *HealthCheckService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *HealthCheckService) GetResults() map[string]*CheckResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	results := make(map[string]*CheckResult, len(s.results))
	for k, v := range s.results {
		cp := *v
		results[k] = &cp
	}
	return results
}

func (s *HealthCheckService) GetResult(name string) *CheckResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if r, ok := s.results[name]; ok {
		cp := *r
		return &cp
	}
	return nil
}

func (s *HealthCheckService) ResetCounter(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.results[name]; ok {
		r.FailureCount = 0
		r.Status = "ok"
		r.InCooldown = false
	}
}

func (s *HealthCheckService) runCheck(ctx context.Context, check config.HealthCheckEntry) {
	interval, _ := time.ParseDuration(check.Interval)
	if interval == 0 {
		interval = 30 * time.Second
	}

	timeout, _ := time.ParseDuration(check.Timeout)
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	cooldown, _ := time.ParseDuration(check.Cooldown)
	if cooldown == 0 {
		cooldown = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.executeCheck(ctx, check, timeout, cooldown)
		}
	}
}

func (s *HealthCheckService) executeCheck(ctx context.Context, check config.HealthCheckEntry, timeout, cooldown time.Duration) {
	s.mu.RLock()
	result := s.results[check.Name]
	if result != nil && result.InCooldown {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	ok, failures := s.probeTargets(ctx, check.Targets, timeout)

	s.mu.Lock()
	if result == nil {
		result = &CheckResult{Name: check.Name}
		s.results[check.Name] = result
	}
	result.LastCheck = time.Now()

	if ok {
		result.Status = "ok"
		result.FailureCount = 0
	} else {
		result.FailureCount++
		result.Status = "failing"
		log.Printf("health check %s: failure %d/%d (%s)",
			check.Name, result.FailureCount, check.FailureThreshold, describeFailures(failures))
	}

	shouldAct := result.FailureCount >= check.FailureThreshold && check.FailureThreshold > 0
	s.mu.Unlock()

	if shouldAct {
		s.executeActions(ctx, check, cooldown)
	}
}

// targetFailure records which target failed and why, so a check that
// fails everywhere can say what it actually tried.
type targetFailure struct {
	target config.HealthCheckTarget
	err    error
}

func (f targetFailure) String() string {
	if f.target.Type == "http" {
		return fmt.Sprintf("http %s: %v", f.target.URL, f.err)
	}
	return fmt.Sprintf("%s %s: %v", f.target.Type, f.target.Host, f.err)
}

// describeFailures renders every failed target onto the single log line
// the check emits, so one line answers what was tried and what each
// target said.
func describeFailures(failures []targetFailure) string {
	if len(failures) == 0 {
		return "no targets configured"
	}
	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "; ")
}

// probeTargets reports whether any target answered, and when none did,
// what each one said.
//
// The errors used to be reduced to a bool per target and then to one
// bool for the whole check, so the only record of a failure was the
// check name and a counter. The shipped wan-internet check probes two
// independent hosts, and once the counter crosses its threshold the
// remediation bounces the interface or reboots the router: an operator
// looking at that afterwards could not tell a genuine WAN outage from an
// upstream that merely filters ICMP.
func (s *HealthCheckService) probeTargets(ctx context.Context, targets []config.HealthCheckTarget, timeout time.Duration) (bool, []targetFailure) {
	failures := make([]targetFailure, 0, len(targets))

	for _, target := range targets {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		var err error

		switch target.Type {
		case "ping":
			_, err = netutil.Run(checkCtx, "ping", "-c", "1", "-W", "3", target.Host)
		case "http":
			err = httpProbe(checkCtx, target.URL, target.ExpectStatus)
		default:
			// A misspelled type used to fail silently and for ever,
			// which reads exactly like an unreachable target.
			err = fmt.Errorf("unsupported target type %q", target.Type)
		}

		cancel()
		if err == nil {
			return true, nil
		}
		failures = append(failures, targetFailure{target: target, err: err})
	}
	return false, failures
}

// httpProbe returns nil when the probe answered as expected. It reports
// its own failures rather than logging them, because only the caller
// knows which check the target belongs to.
func httpProbe(ctx context.Context, url string, expectStatus int) error {
	if expectStatus == 0 {
		expectStatus = 204
	}

	if err := validateOutboundURL(url); err != nil {
		return fmt.Errorf("rejected probe URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := outboundProbeClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != expectStatus {
		return fmt.Errorf("status %d, want %d", resp.StatusCode, expectStatus)
	}
	return nil
}

func (s *HealthCheckService) executeActions(ctx context.Context, check config.HealthCheckEntry, cooldown time.Duration) {
	for _, action := range check.Actions {
		delay, _ := time.ParseDuration(action.Delay)
		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}

		log.Printf("health check %s: executing action %s", check.Name, action.Type)

		s.mu.Lock()
		if r, ok := s.results[check.Name]; ok {
			r.LastAction = action.Type
			r.LastActionAt = time.Now()
		}
		s.mu.Unlock()

		var err error
		switch action.Type {
		case "restartInterface":
			err = s.actionRestartInterface(ctx, check.Interface)
		case "restartPppoe":
			err = s.actionRestartPPPoE(ctx)
		case "failoverUsb":
			err = s.actionFailoverUSB(ctx)
		case "rebootSystem":
			err = s.actionReboot(ctx)
		default:
			log.Printf("health check %s: unknown action %s", check.Name, action.Type)
			continue
		}

		if err != nil {
			log.Printf("health check %s: action %s failed: %v", check.Name, action.Type, err)
			continue
		}

		s.mu.Lock()
		if r, ok := s.results[check.Name]; ok {
			r.InCooldown = true
			r.FailureCount = 0
		}
		s.mu.Unlock()

		go func() {
			time.Sleep(cooldown)
			s.mu.Lock()
			if r, ok := s.results[check.Name]; ok {
				r.InCooldown = false
			}
			s.mu.Unlock()
		}()

		return
	}
}

func (s *HealthCheckService) actionRestartInterface(ctx context.Context, ifaceID string) error {
	var device string
	for _, iface := range s.cfg.Interfaces {
		if iface.ID == ifaceID {
			device = iface.Device
			break
		}
	}
	if device == "" {
		return fmt.Errorf("interface %s not found", ifaceID)
	}

	if _, err := netutil.Run(ctx, "ip", "link", "set", device, "down"); err != nil {
		log.Printf("healthcheck: link down %s: %v", device, err)
	}
	time.Sleep(2 * time.Second)
	_, err := netutil.Run(ctx, "ip", "link", "set", device, "up")
	return err
}

func (s *HealthCheckService) actionRestartPPPoE(ctx context.Context) error {
	if _, err := netutil.Run(ctx, "killall", "pppd"); err != nil {
		log.Printf("healthcheck: killall pppd: %v", err)
	}
	time.Sleep(3 * time.Second)
	_, err := netutil.Run(ctx, "pppd", "call", "wan")
	return err
}

func (s *HealthCheckService) actionFailoverUSB(ctx context.Context) error {
	iface := s.cfg.USBTether.Interface
	if iface == "" {
		iface = "usb0"
	}

	state, err := netutil.GetInterfaceState(iface)
	if err != nil || state != "up" {
		return fmt.Errorf("USB interface %s not available", iface)
	}

	_, err = netutil.Run(ctx, "dhclient", "-1", iface)
	if err != nil {
		return fmt.Errorf("dhclient USB: %w", err)
	}

	metric := s.cfg.USBTether.Metric
	if metric == 0 {
		metric = 100
	}

	_, err = netutil.Run(ctx, "ip", "route", "replace", "default", "dev", iface,
		"metric", fmt.Sprintf("%d", metric))
	return err
}

func (s *HealthCheckService) actionReboot(ctx context.Context) error {
	log.Println("HEALTH CHECK: initiating system reboot")
	_, err := netutil.Run(ctx, "systemctl", "reboot")
	return err
}
