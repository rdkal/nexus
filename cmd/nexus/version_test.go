package main

import "testing"

func TestResolveVersionInjected(t *testing.T) {
	// Release builds inject the tag via -ldflags "-X main.version=...".
	orig := version
	t.Cleanup(func() { version = orig })

	version = "v1.2.3"
	if got := resolveVersion(); got != "v1.2.3" {
		t.Errorf("injected version: got %q, want v1.2.3", got)
	}
}

func TestResolveVersionFallback(t *testing.T) {
	// With no injected version, resolveVersion falls back to build info and
	// must still return a non-empty string (never a bare empty version).
	orig := version
	t.Cleanup(func() { version = orig })

	version = ""
	if got := resolveVersion(); got == "" {
		t.Error("fallback version should never be empty")
	}
}
