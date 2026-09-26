package config_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// The shipped router.yaml is what an operator reads to learn what the
// router does, so every key in it must decode into a field, and keys
// that once described features no code implements must stay out.
func TestShippedRouterYAMLDescribesOnlyRealSettings(t *testing.T) {
	raw, err := os.ReadFile("../../configs/defaults/router.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg config.Config
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("shipped router.yaml has a key no field reads: %v", err)
	}
	for _, key := range []string{"failureWindow:", "notify:", "autoFailback:", "failoverDelay:",
		"failbackDelay:", "logBlocked:", "caInstalled:", "autoConnect:", "rateLimits:"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("shipped router.yaml sets %s, which nothing implements", key)
		}
	}
}
