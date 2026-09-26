package config

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/KilimcininKorOglu/lankeeper/configs"
)

// DefaultConfig returns the shipped router.yaml, parsed from the copy
// embedded in the binary. There is one set of defaults: a second copy
// written out in Go drifted from the shipped file, and every test built
// on it exercised values no installed router has.
//
// It panics when the embedded file does not parse, which only a broken
// build can cause, and every test that calls it reports that at once.
func DefaultConfig() *Config {
	raw, err := configs.DefaultsFS.ReadFile("defaults/router.yaml")
	if err != nil {
		panic(fmt.Sprintf("read embedded router.yaml: %v", err))
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		panic(fmt.Sprintf("parse embedded router.yaml: %v", err))
	}
	return &cfg
}
