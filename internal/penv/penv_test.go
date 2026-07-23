package penv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rdkal/nexus/internal/home"
)

func find(env []string, key string) (string, bool) {
	for _, e := range env {
		if k, v, ok := cut(e); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func cut(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func TestBuildContractAndVolumes(t *testing.T) {
	paths := home.NewPaths(t.TempDir())
	env := Build(Input{
		Paths:      paths,
		Address:    "traefik",
		Ref:        "main",
		SHA:        "abc123",
		WorkDir:    "/wt",
		OwnVolumes: map[string]struct{}{"dynamic": {}},
	})
	if v, _ := find(env, "NEXUS_PROJECT"); v != "traefik" {
		t.Errorf("NEXUS_PROJECT = %q", v)
	}
	if v, ok := find(env, "NEXUS_VOLUME_DYNAMIC"); !ok || v != paths.VolumeDir("traefik", "dynamic") {
		t.Errorf("NEXUS_VOLUME_DYNAMIC = %q ok=%v", v, ok)
	}
}

func TestVolumeVar(t *testing.T) {
	if got := VolumeVar("traefik", "dynamic"); got != "NEXUS_TRAEFIK_DYNAMIC" {
		t.Errorf("got %q", got)
	}
	if got := VolumeVar("my-system/db", "data"); got != "NEXUS_MY_SYSTEM_DB_DATA" {
		t.Errorf("got %q", got)
	}
}

func TestGlobalVolumeAndInterpolation(t *testing.T) {
	// Authelia references Traefik's volume by the global var, remapped to its own.
	env := Build(Input{
		Paths:         home.NewPaths(t.TempDir()),
		Address:       "authelia",
		WorkDir:       "/wt",
		GlobalVolumes: map[string]string{"NEXUS_TRAEFIK_DYNAMIC": "/vol/traefik/dynamic"},
		ServiceEnv:    map[string]string{"X_ROUTES_DIR": "${NEXUS_TRAEFIK_DYNAMIC}/authelia"},
	})
	if v, _ := find(env, "X_ROUTES_DIR"); v != "/vol/traefik/dynamic/authelia" {
		t.Errorf("interpolated X_ROUTES_DIR = %q", v)
	}
}

func TestServiceOverridesProjectOverridesDotenv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("K=from_dotenv\nONLY_DOTENV=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := Build(Input{
		Paths:      home.NewPaths(t.TempDir()),
		Address:    "app",
		WorkDir:    dir,
		ProjectEnv: map[string]string{"K": "from_project"},
		ServiceEnv: map[string]string{"K": "from_service"},
	})
	if v, _ := find(env, "K"); v != "from_service" {
		t.Errorf("service should win: K = %q", v)
	}
	if v, ok := find(env, "ONLY_DOTENV"); !ok || v != "1" {
		t.Errorf("dotenv-only var missing: %q ok=%v", v, ok)
	}
}

func TestNexusContractNotOverridable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("NEXUS_PROJECT=hacked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := Build(Input{
		Paths:      home.NewPaths(t.TempDir()),
		Address:    "app",
		WorkDir:    dir,
		ProjectEnv: map[string]string{"NEXUS_SHA": "hacked"},
	})
	if v, _ := find(env, "NEXUS_PROJECT"); v != "app" {
		t.Errorf("NEXUS_PROJECT should stay authoritative, got %q", v)
	}
	if v, _ := find(env, "NEXUS_SHA"); v == "hacked" {
		t.Errorf("NEXUS_SHA should not be overridable by environment:")
	}
}

func TestInterpolate(t *testing.T) {
	lookup := map[string]string{"A": "1", "B_C": "two"}
	cases := map[string]string{
		"${A}":        "1",
		"$A":          "1",
		"${A}/${B_C}": "1/two",
		"$B_C-x":      "two-x",
		"$$A":         "$A",
		"lone $ ok":   "lone $ ok",
		"${MISSING}":  "",
		"no vars":     "no vars",
	}
	for in, want := range cases {
		if got := interpolate(in, lookup); got != want {
			t.Errorf("interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadDotenv(t *testing.T) {
	dir := t.TempDir()
	content := "# comment\n\nexport A=1\nB = two \nC=\"quoted value\"\nD='single'\nBAD LINE\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m := readDotenv(filepath.Join(dir, ".env"))
	want := map[string]string{"A": "1", "B": "two", "C": "quoted value", "D": "single"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["BAD"]; ok {
		t.Error("line without = should be skipped")
	}
}
