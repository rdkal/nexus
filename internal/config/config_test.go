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
