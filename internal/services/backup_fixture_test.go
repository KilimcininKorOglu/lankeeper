package services

import (
	"testing"

	"github.com/KilimcininKorOglu/lankeeper/internal/config"
	"gopkg.in/yaml.v3"
)

// validRouterYAML is a router.yaml that passes config.Validate, as an
// archive's config must before Import installs anything.
func validRouterYAML(t *testing.T) string {
	t.Helper()
	data, err := yaml.Marshal(config.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
