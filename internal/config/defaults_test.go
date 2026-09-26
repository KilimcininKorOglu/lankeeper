package config_test

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
)

// Tests build on DefaultConfig, so it has to be the config an installed
// router starts from: the shipped router.yaml, not a second copy that
// drifts from it.
func TestDefaultConfigIsTheShippedRouterYAML(t *testing.T) {
	raw, err := os.ReadFile("../../configs/defaults/router.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var shipped config.Config
	if err := yaml.Unmarshal(raw, &shipped); err != nil {
		t.Fatal(err)
	}
	a, _ := yaml.Marshal(&shipped)
	b, _ := yaml.Marshal(config.DefaultConfig())
	if string(a) != string(b) {
		t.Error("DefaultConfig differs from configs/defaults/router.yaml")
	}
	if err := config.DefaultConfig().Validate(); err != nil {
		t.Errorf("the shipped config does not validate: %v", err)
	}
}
