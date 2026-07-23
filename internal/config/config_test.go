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

func TestSubProject_StringShorthand(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
projects:
  db: github.com/community/postgres@v15
  cache: github.com/community/redis
  metrics:
    services:
      exporter:
        run: ./exporter
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	// String with @ref → external, spec + ref split on '@'.
	db := f.Projects["db"]
	if !db.IsExternal() || db.Src != "github.com/community/postgres" || db.Ref != "v15" {
		t.Errorf("db shorthand = %+v", db)
	}
	// Bare string → external, no ref (caller applies default).
	cache := f.Projects["cache"]
	if !cache.IsExternal() || cache.Src != "github.com/community/redis" || cache.Ref != "" {
		t.Errorf("cache shorthand = %+v", cache)
	}
	// Map form still parses as before (inline here).
	metrics := f.Projects["metrics"]
	if metrics.IsExternal() || metrics.Services["exporter"].Run != "./exporter" {
		t.Errorf("metrics map = %+v", metrics)
	}
}

func TestSubProject_MapFormExternal(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
projects:
  db:
    src: github.com/community/postgres
    ref: v15
`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	db := f.Projects["db"]
	if !db.IsExternal() || db.Src != "github.com/community/postgres" || db.Ref != "v15" {
		t.Errorf("db map = %+v", db)
	}
}

func TestParseEnvironmentMapForm(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
environment:
  LOG_LEVEL: info
  PORT: 8080
services:
  api:
    run: ./api
    environment:
      API_KEY: secret
`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Environment["LOG_LEVEL"] != "info" {
		t.Errorf("project LOG_LEVEL = %q", f.Environment["LOG_LEVEL"])
	}
	if f.Environment["PORT"] != "8080" { // numbers stringify
		t.Errorf("project PORT = %q, want \"8080\"", f.Environment["PORT"])
	}
	if f.Services["api"].Environment["API_KEY"] != "secret" {
		t.Errorf("service API_KEY = %q", f.Services["api"].Environment["API_KEY"])
	}
}

func TestParseEnvironmentListForm(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
services:
  api:
    run: ./api
    environment:
      - API_KEY=secret
      - EMPTY=
`))
	if err != nil {
		t.Fatal(err)
	}
	env := f.Services["api"].Environment
	if env["API_KEY"] != "secret" {
		t.Errorf("API_KEY = %q", env["API_KEY"])
	}
	if v, ok := env["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY = %q ok=%v", v, ok)
	}
}

func TestFlattenCarriesEnvironment(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
environment:
  ROOT: 1
projects:
  metrics:
    environment:
      SUB: 2
    services:
      exp:
        run: ./exp
`))
	if err != nil {
		t.Fatal(err)
	}
	units, _ := f.Flatten()
	var base, metrics *config.InlineUnit
	for i := range units {
		switch len(units[i].RelPath) {
		case 0:
			base = &units[i]
		case 1:
			metrics = &units[i]
		}
	}
	if base == nil || base.Environment["ROOT"] != "1" {
		t.Errorf("base env = %+v", base)
	}
	if metrics == nil || metrics.Environment["SUB"] != "2" {
		t.Errorf("metrics env = %+v", metrics)
	}
}

func TestParseEnvironmentBareKeyForwards(t *testing.T) {
	f, err := config.ParseBytes([]byte(`
services:
  api:
    run: ./api
    environment:
      - CF_TOKEN
`))
	if err != nil {
		t.Fatal(err)
	}
	// A bare key becomes ${KEY}, which penv resolves from the daemon environment.
	if got := f.Services["api"].Environment["CF_TOKEN"]; got != "${CF_TOKEN}" {
		t.Errorf("bare key forward = %q, want ${CF_TOKEN}", got)
	}
}
