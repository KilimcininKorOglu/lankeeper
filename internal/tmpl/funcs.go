package tmpl

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/i18n"
)

func FuncMap(loc *i18n.I18n) template.FuncMap {
	return template.FuncMap{
		"t": func(lang, key string) string {
			return loc.T(lang, key)
		},
		"tp": func(lang, key string, args ...string) string {
			if len(args)%2 != 0 {
				return loc.T(lang, key)
			}
			params := make(map[string]string, len(args)/2)
			for i := 0; i < len(args); i += 2 {
				params[args[i]] = args[i+1]
			}
			return loc.WithParams(lang, key, params)
		},
		"eq": func(a, b string) bool {
			return a == b
		},
		"formatBytes": formatBytes,
		"humanTime":   humanTime,
		"upper":       strings.ToUpper,
		"lower":       strings.ToLower,
		"join":        strings.Join,
	}
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanTime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
