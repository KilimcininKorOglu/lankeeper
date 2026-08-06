// Package configs embeds the shipped default configuration so the
// binary carries its own factory state. Factory reset is a recovery
// tool: it must not depend on a directory mirrored onto the target at
// install time, which may be missing, damaged, or left behind at an
// older version by an OTA update that only replaces the binary.
package configs

import "embed"

//go:embed defaults/*.yaml
var DefaultsFS embed.FS
