package services

import "testing"

// TestClampCacheSize covers the shipped default that rendered Unbound
// caches far beyond the documented hardware. The field carries no unit
// and the template appends "m", so a value written as kilobytes becomes
// that many megabytes across three directives.
func TestClampCacheSize(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unset falls back to the default", 0, defaultDNSCacheSizeMB},
		{"the old shipped default is clamped", 50000, maxDNSCacheSizeMB},
		{"a sane value passes through", 64, 64},
		{"the ceiling passes through", maxDNSCacheSizeMB, maxDNSCacheSizeMB},
		{"above the ceiling is clamped", maxDNSCacheSizeMB + 1, maxDNSCacheSizeMB},
		{"below the floor is raised", 1, minDNSCacheSizeMB},
		{"negative is raised to the floor", -8, minDNSCacheSizeMB},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampCacheSize(tc.in); got != tc.want {
				t.Errorf("clampCacheSize(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestClampedCacheFitsDocumentedHardware pins the ceiling to the reason
// it exists: README documents 4 GB RAM as the minimum, and the template
// multiplies the configured value across three caches.
func TestClampedCacheFitsDocumentedHardware(t *testing.T) {
	const minimumSystemRAMMB = 4096

	base := clampCacheSize(maxDNSCacheSizeMB)
	// msg-cache-size + rrset-cache-size (2x) + key-cache-size
	total := base + base*2 + base

	if total >= minimumSystemRAMMB/2 {
		t.Errorf("worst-case cache request is %d MB, at least half the documented %d MB minimum",
			total, minimumSystemRAMMB)
	}
}
