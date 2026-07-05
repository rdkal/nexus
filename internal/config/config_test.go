package config_test

import (
	"testing"

	"github.com/rdkal/nexus/internal/config"
)

func TestParseMinimal(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
services:
  server:
    run: python -m http.server
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	svc, ok := f.Services["server"]
	if !ok {
		t.Fatal("expected service 'server'")
	}
	if svc.Run != "python -m http.server" {
		t.Errorf("run = %q, want %q", svc.Run, "python -m http.server")
	}
}

func TestParseExternalProject(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
projects:
  db:
    src: github.com/nexus-community/postgres
    ref: "@v15"
  api:
    src: github.com/myorg/api
    ref: "@main"
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	db := f.Projects["db"]
	if !db.IsExternal() {
		t.Error("db should be external")
	}
	if db.Src != "github.com/nexus-community/postgres" {
		t.Errorf("db.src = %q", db.Src)
	}
	if db.Ref != "@v15" {
		t.Errorf("db.ref = %q, want %q", db.Ref, "@v15")
	}
}

func TestParseInlineProject(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
projects:
  metrics:
    services:
      exporter:
        run: ./metrics-exporter
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	metrics := f.Projects["metrics"]
	if metrics.IsExternal() {
		t.Error("metrics should be inline (no src)")
	}
	svc, ok := metrics.Services["exporter"]
	if !ok {
		t.Fatal("expected service 'exporter' in inline project")
	}
	if svc.Run != "./metrics-exporter" {
		t.Errorf("run = %q, want %q", svc.Run, "./metrics-exporter")
	}
}

func TestParseVolumes(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
volumes:
  data: {}
  uploads: {}
services:
  pg:
    run: postgres -D $NEXUS_VOLUME_DATA
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if _, ok := f.Volumes["data"]; !ok {
		t.Error("expected volume 'data'")
	}
	if _, ok := f.Volumes["uploads"]; !ok {
		t.Error("expected volume 'uploads'")
	}
}

func TestParseBuild(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
build: pip install -e . && alembic upgrade head
services:
  api:
    run: uvicorn app:main
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if f.Build != "pip install -e . && alembic upgrade head" {
		t.Errorf("build = %q", f.Build)
	}
}

func TestParseNestedProjects(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
projects:
  db:
    src: github.com/nexus-community/postgres
    ref: "@v15"
  worker:
    build: pip install -e tools/worker
    services:
      bg:
        run: python -m worker
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if !f.Projects["db"].IsExternal() {
		t.Error("db should be external")
	}
	if f.Projects["worker"].IsExternal() {
		t.Error("worker should be inline")
	}
	if f.Projects["worker"].Services["bg"].Run != "python -m worker" {
		t.Error("wrong run command for inline worker service")
	}
}

func TestFlatten_InlineAndExternal(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
build: make root
volumes:
  data: {}
services:
  api:
    run: ./api
projects:
  db:                       # external — not recursed into, listed as external
    src: github.com/community/postgres
    ref: "@v15"
  metrics:                  # inline — its build/services join the deployment
    build: pip install exporter
    services:
      exporter:
        run: ./exporter
    projects:
      probe:                # inline nested inside inline
        services:
          ping:
            run: ./ping
      remote:               # external nested inside inline
        src: github.com/community/remote
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	units, external := f.Flatten()

	// Units: base (""), metrics, metrics/probe — sorted, base first.
	wantUnits := []string{"", "metrics", "metrics/probe"}
	if len(units) != len(wantUnits) {
		t.Fatalf("expected %d units, got %d: %+v", len(wantUnits), len(units), units)
	}
	for i, want := range wantUnits {
		if got := joinRelForTest(units[i].RelPath); got != want {
			t.Errorf("unit[%d] rel = %q, want %q", i, got, want)
		}
	}
	// Base unit carries root build/services.
	if units[0].Build != "make root" || units[0].Services["api"].Run != "./api" {
		t.Errorf("base unit wrong: %+v", units[0])
	}
	// Inline metrics unit carries its own build/services.
	if units[1].Build != "pip install exporter" || units[1].Services["exporter"].Run != "./exporter" {
		t.Errorf("metrics unit wrong: %+v", units[1])
	}
	if units[2].Services["ping"].Run != "./ping" {
		t.Errorf("probe unit wrong: %+v", units[2])
	}

	// External: db (top-level) and metrics/remote (nested in inline) — sorted.
	wantExt := []string{"db", "metrics/remote"}
	if len(external) != len(wantExt) {
		t.Fatalf("expected %d external, got %d: %+v", len(wantExt), len(external), external)
	}
	for i, want := range wantExt {
		if got := joinRelForTest(external[i].RelPath); got != want {
			t.Errorf("external[%d] rel = %q, want %q", i, got, want)
		}
	}
	if external[0].Src != "github.com/community/postgres" || external[0].Ref != "@v15" {
		t.Errorf("db external wrong: %+v", external[0])
	}
}

func joinRelForTest(rel []string) string {
	out := ""
	for i, s := range rel {
		if i > 0 {
			out += "/"
		}
		out += s
	}
	return out
}
