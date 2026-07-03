package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rdkal/nexus/internal/git"
)

// makeUpstream creates a local git repo with one commit on main and returns its path.
func makeUpstream(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}

	gitCmd("init")
	gitCmd("checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd("add", ".")
	gitCmd("commit", "-m", "initial")

	return dir
}

// addTag creates a lightweight tag in the repo at dir.
func addTag(t *testing.T, dir, tag string) {
	t.Helper()
	cmd := exec.Command("git", "tag", tag)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag %s: %s", tag, out)
	}
}

func TestEnsureBareClone_Creates(t *testing.T) {
	upstream := makeUpstream(t)
	cloneDir := filepath.Join(t.TempDir(), "bare")

	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("EnsureBareClone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cloneDir, "HEAD")); err != nil {
		t.Fatalf("bare clone missing HEAD: %v", err)
	}
}

func TestEnsureBareClone_Idempotent(t *testing.T) {
	upstream := makeUpstream(t)
	cloneDir := filepath.Join(t.TempDir(), "bare")

	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("first EnsureBareClone: %v", err)
	}
	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("second EnsureBareClone: %v", err)
	}
}

func TestResolveRef_Branch(t *testing.T) {
	upstream := makeUpstream(t)
	cloneDir := filepath.Join(t.TempDir(), "bare")
	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("EnsureBareClone: %v", err)
	}

	sha, err := git.ResolveRef(cloneDir, "@main")
	if err != nil {
		t.Fatalf("ResolveRef(@main): %v", err)
	}
	if len(sha) < 7 {
		t.Errorf("SHA looks short: %q", sha)
	}
}

func TestResolveRef_Latest(t *testing.T) {
	upstream := makeUpstream(t)
	addTag(t, upstream, "v1.0.0")

	cloneDir := filepath.Join(t.TempDir(), "bare")
	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("EnsureBareClone: %v", err)
	}

	sha, err := git.ResolveRef(cloneDir, "@latest")
	if err != nil {
		t.Fatalf("ResolveRef(@latest): %v", err)
	}
	if len(sha) < 7 {
		t.Errorf("SHA looks short: %q", sha)
	}
}

func TestWorktreeAddRemove(t *testing.T) {
	upstream := makeUpstream(t)
	cloneDir := filepath.Join(t.TempDir(), "bare")
	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("EnsureBareClone: %v", err)
	}

	sha, err := git.ResolveRef(cloneDir, "@main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := git.WorktreeAdd(cloneDir, worktreePath, sha); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README")); err != nil {
		t.Fatalf("README not in worktree: %v", err)
	}

	if err := git.WorktreeRemove(cloneDir, worktreePath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Error("worktree dir still exists after remove")
	}
}

// TestWorktreeAdd_Idempotent verifies that re-adding a worktree that already
// exists at the same path (as after a deploy interrupted by a restart) succeeds
// and leaves the checkout intact, rather than failing with "already exists".
func TestWorktreeAdd_Idempotent(t *testing.T) {
	upstream := makeUpstream(t)
	cloneDir := filepath.Join(t.TempDir(), "bare")
	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("EnsureBareClone: %v", err)
	}
	sha, err := git.ResolveRef(cloneDir, "@main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := git.WorktreeAdd(cloneDir, worktreePath, sha); err != nil {
		t.Fatalf("first WorktreeAdd: %v", err)
	}

	// A second add at the same path must be a no-op success (worktree reused).
	if err := git.WorktreeAdd(cloneDir, worktreePath, sha); err != nil {
		t.Fatalf("second WorktreeAdd should reuse existing worktree, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README")); err != nil {
		t.Fatalf("README missing after idempotent add: %v", err)
	}
}

// TestWorktreeAdd_ClearsLeftoverDir verifies that a stray directory without
// worktree metadata (e.g. a partially created worktree) is cleared and recreated.
func TestWorktreeAdd_ClearsLeftoverDir(t *testing.T) {
	upstream := makeUpstream(t)
	cloneDir := filepath.Join(t.TempDir(), "bare")
	if err := git.EnsureBareClone(cloneDir, upstream); err != nil {
		t.Fatalf("EnsureBareClone: %v", err)
	}
	sha, err := git.ResolveRef(cloneDir, "@main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	worktreePath := filepath.Join(t.TempDir(), "wt")
	// Simulate a leftover directory with no .git metadata.
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := git.WorktreeAdd(cloneDir, worktreePath, sha); err != nil {
		t.Fatalf("WorktreeAdd over leftover dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README")); err != nil {
		t.Fatalf("README missing after recreating worktree: %v", err)
	}
}
