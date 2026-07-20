package home_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdkal/nexus/internal/home"
)

func TestNewPaths(t *testing.T) {
	p := home.NewPaths("/nh")
	cases := []struct{ name, got, want string }{
		{"DB", p.DB, "/nh/nexus.db"},
		{"Socket", p.Socket, "/nh/nexus.sock"},
		{"Repos", p.Repos, "/nh/repos"},
		{"Volumes", p.Volumes, "/nh/volumes"},
		{"Logs", p.Logs, "/nh/logs"},
		{"Bin", p.Bin, "/nh/bin"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestVolumeDir(t *testing.T) {
	p := home.NewPaths("/nh")
	got := p.VolumeDir("my-system/db", "data")
	want := "/nh/volumes/my-system/db/data"
	if got != want {
		t.Errorf("VolumeDir = %q, want %q", got, want)
	}
}

func TestBuildLog(t *testing.T) {
	p := home.NewPaths("/nh")
	got := p.BuildLog("my-system/db", "abc123")
	want := "/nh/logs/my-system/db/abc123-build.log"
	if got != want {
		t.Errorf("BuildLog = %q, want %q", got, want)
	}
}

func TestServiceLog(t *testing.T) {
	p := home.NewPaths("/nh")
	got := p.ServiceLog("my-system/db/postgres")
	want := "/nh/logs/my-system/db/postgres/current.log"
	if got != want {
		t.Errorf("ServiceLog = %q, want %q", got, want)
	}
}

func TestRepoDir(t *testing.T) {
	p := home.NewPaths("/nh")
	got := p.RepoDir("github.com/myorg/api")
	want := "/nh/repos/github.com/myorg/api"
	if got != want {
		t.Errorf("RepoDir = %q, want %q", got, want)
	}
}

func TestWorktreeDir(t *testing.T) {
	p := home.NewPaths("/nh")
	cases := []struct {
		specPath string
		aliases  []string
		sha      string
		want     string
	}{
		{
			"github.com/myorg/my-system", nil, "abc123",
			"/nh/repos/github.com/myorg/my-system/worktrees/abc123",
		},
		{
			"github.com/myorg/my-system", []string{"db"}, "abc123",
			"/nh/repos/github.com/myorg/my-system/db/worktrees/abc123",
		},
		{
			"github.com/myorg/my-system", []string{"api", "shared-lib"}, "def456",
			"/nh/repos/github.com/myorg/my-system/api/shared-lib/worktrees/def456",
		},
	}
	for _, c := range cases {
		got := p.WorktreeDir(c.specPath, c.aliases, c.sha)
		if got != c.want {
			t.Errorf("WorktreeDir(%q, %v, %q) = %q, want %q", c.specPath, c.aliases, c.sha, got, c.want)
		}
	}
}

func TestCheckSocketPath(t *testing.T) {
	if err := home.CheckSocketPath("/home/user/.nexus/nexus.sock"); err != nil {
		t.Errorf("short path should pass: %v", err)
	}
	// Exactly at the limit passes; one over fails.
	atLimit := strings.Repeat("a", home.MaxSocketPath)
	if err := home.CheckSocketPath(atLimit); err != nil {
		t.Errorf("path at the limit (%d) should pass: %v", home.MaxSocketPath, err)
	}
	over := strings.Repeat("a", home.MaxSocketPath+1)
	err := home.CheckSocketPath(over)
	if err == nil {
		t.Fatal("path over the limit should fail")
	}
	if !strings.Contains(err.Error(), "NEXUS_HOME") {
		t.Errorf("error should guide the user toward NEXUS_HOME, got: %v", err)
	}
}

func TestSetupIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := home.Setup(dir); err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	if err := home.Setup(dir); err != nil {
		t.Fatalf("second Setup (idempotent): %v", err)
	}
	for _, sub := range []string{"bin", "repos", "volumes", "logs"} {
		full := filepath.Join(dir, sub)
		info, err := filepath.Abs(full)
		if err != nil || info == "" {
			t.Errorf("expected %s to exist", full)
		}
	}
}
