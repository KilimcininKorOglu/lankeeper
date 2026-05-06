package services

import "testing"

func TestBuiltInDoHResolversNonEmpty(t *testing.T) {
	got := BuiltInDoHResolvers()
	if len(got) == 0 {
		t.Fatal("expected non-empty catalogue")
	}
	seen := map[string]bool{}
	for _, r := range got {
		if r.Name == "" {
			t.Errorf("entry missing Name: %+v", r)
		}
		if r.Description == "" {
			t.Errorf("entry missing Description: %+v", r)
		}
		if seen[r.Name] {
			t.Errorf("duplicate Name %q", r.Name)
		}
		seen[r.Name] = true
	}
}

func TestIsBuiltInDoHResolver(t *testing.T) {
	if !IsBuiltInDoHResolver("cloudflare") {
		t.Error("cloudflare should be in catalogue")
	}
	if IsBuiltInDoHResolver("nonexistent") {
		t.Error("nonexistent should not be in catalogue")
	}
	if IsBuiltInDoHResolver("") {
		t.Error("empty string should not match")
	}
}
