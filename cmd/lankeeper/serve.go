package main

import (
	"context"
	crypto_rand "crypto/rand"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/KilimcininKorOglu/lankeeper/internal/agent"
	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
	"github.com/KilimcininKorOglu/lankeeper/internal/services"
	"github.com/KilimcininKorOglu/lankeeper/internal/web"
	webFS "github.com/KilimcininKorOglu/lankeeper/web"
)

func runServe() error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "/etc/lankeeper/router.yaml", "config file path")
	socketPath := fs.String("socket", "/run/lankeeper/agent.sock", "agent UDS path")
	cwd := fs.String("cwd", "/var/lib/lankeeper", "directory holding the configs/sysconf/ templates the services parse at runtime")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	// Every service parses configs/sysconf/*.tmpl relative to the working
	// directory, and the installers put the templates only under the data
	// directory. systemd starts the unit in /, where no service could
	// render a config at runtime.
	if err := os.Chdir(*cwd); err != nil {
		return fmt.Errorf("chdir %s: %w", *cwd, err)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := ensureSessionSecret(cfg); err != nil {
		return err
	}

	loc, err := loadLocalizer(cfg.System.Language)
	if err != nil {
		return err
	}

	agentClient := agent.NewClient(*socketPath)
	netutil.SetAgentClient(agentClient)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	runFirstBoot(ctx, cfg)

	backupSvc := services.NewBackupService("/etc/lankeeper")
	updateSvc := services.NewUpdateService(version, commit, date, backupSvc)

	srv, err := web.NewServer(cfg, loc, webFS.EmbeddedFS, updateSvc)
	if err != nil {
		return fmt.Errorf("failed to create web server: %w", err)
	}

	log.Printf("lankeeper serve starting (bind=%s:%d, lang=%s)",
		cfg.System.WebBind, cfg.System.WebPort, loc.Fallback())

	return srv.Serve(ctx)
}

// ensureSessionSecret generates and persists a session secret when the
// config has none.
//
// The read error is checked even though crypto/rand.Read on this
// toolchain never returns one and crashes the program if its source
// fails. This value signs every session cookie, so refusing to start is
// the only acceptable answer to a failure here: a secret built from an
// unfilled buffer would let anyone mint a valid session.
func ensureSessionSecret(cfg *config.Config) error {
	if cfg.System.SessionSecret != "" {
		return nil
	}
	b := make([]byte, 32)
	if _, err := crypto_rand.Read(b); err != nil {
		return fmt.Errorf("generate session secret: %w", err)
	}
	cfg.System.SessionSecret = fmt.Sprintf("%x", b)
	if err := cfg.SaveToFile(); err != nil {
		log.Printf("failed to persist session secret: %v", err)
	} else {
		log.Println("generated and persisted random session secret")
	}
	return nil
}

// loadLocalizer builds the localizer and loads the embedded locales.
func loadLocalizer(lang string) (*i18n.I18n, error) {
	loc, err := i18n.New(lang)
	if err != nil {
		return nil, fmt.Errorf("failed to init i18n: %w", err)
	}
	if err := loc.LoadFromFS(webFS.EmbeddedFS, "locales"); err != nil {
		return nil, fmt.Errorf("failed to load locales: %w", err)
	}
	return loc, nil
}

// runFirstBoot bridges every physical NIC when the first-boot flag is
// set.
//
// The shipped config names the build machine's network cards, so on
// unfamiliar hardware there is no port the UI answers on until the
// operator corrects it. Bridging every physical NIC and holding
// 10.10.10.1 makes the UI reachable from any port so they can.
//
// Not fatal. This exists to make the first login easier; failing the
// whole start over it would take DNS, DHCP and the firewall down to
// protect a convenience. An operator who can already reach the UI loses
// nothing when it does not run.
func runFirstBoot(ctx context.Context, cfg *config.Config) {
	firstBoot := services.NewFirstBootService(cfg)
	if !firstBoot.IsActive() {
		return
	}
	nics, err := firstBoot.Setup(ctx)
	if err != nil {
		log.Printf("first-boot: bridge setup failed, continuing without it: %v", err)
		return
	}
	log.Printf("first-boot: %d NIC(s) bridged, UI reachable on any port at 10.10.10.1", len(nics))
}
