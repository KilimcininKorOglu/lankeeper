package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/KilimcininKorOglu/home-router/internal/config"
	"github.com/KilimcininKorOglu/home-router/internal/services"
)

// runRenderConfigs renders every service-managed /etc/* config file from the
// home-router templates and exits. It does NOT reload any service. Suitable
// for install-time invocation: run before native Debian services start so
// they come up on first boot using home-router-managed configs.
func runRenderConfigs() error {
	fs := flag.NewFlagSet("render-configs", flag.ExitOnError)
	configPath := fs.String("config", "/etc/home-router/router.yaml", "config file path")
	sysconfDir := fs.String("sysconf-dir", "", "directory whose parent will become CWD so template.ParseFiles(\"configs/sysconf/*.tmpl\") resolves")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	if *sysconfDir != "" {
		// Services use template.ParseFiles("configs/sysconf/...") relative to
		// CWD. Setting CWD to the parent of `--sysconf-dir` makes that path
		// resolve when run from /var/lib/home-router/sysconf/.. (install time).
		parent := *sysconfDir + "/.."
		if err := os.Chdir(parent); err != nil {
			return fmt.Errorf("chdir %s: %w", parent, err)
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	ctx := context.Background()

	type renderer struct {
		name string
		fn   func(context.Context) error
	}
	steps := []renderer{
		{"dns/unbound", services.NewDNSService(cfg).RenderToDisk},
		{"dhcp/dnsmasq", services.NewDHCPService(cfg).RenderToDisk},
		{"ntp/chrony", services.NewNTPService(cfg).RenderToDisk},
		{"syslog/rsyslog", services.NewSyslogService(cfg).RenderToDisk},
		{"nas/samba", services.NewNASService(cfg).RenderToDisk},
		{"vpn/wireguard-server", services.NewVPNService(cfg).RenderServerConfig},
		{"vpn/wireguard-clients", services.NewVPNService(cfg).RenderAllClientConfigs},
	}

	failures := 0
	for _, step := range steps {
		if err := step.fn(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "render %s: %v\n", step.name, err)
			failures++
			continue
		}
		fmt.Printf("rendered %s\n", step.name)
	}

	if failures > 0 {
		return fmt.Errorf("%d render step(s) failed", failures)
	}
	return nil
}
