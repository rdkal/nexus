package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureBareClone ensures a bare clone of remote exists at repoDir.
// If a clone already exists, it runs git fetch --prune to update it.
// remote is a spec path (e.g. "github.com/myorg/api"); git resolves the transport.
func EnsureBareClone(repoDir, remote string) error {
	if isBareClone(repoDir) {
		_, err := run(repoDir, "fetch", "--prune", "origin")
		return err
	}
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	_, err := run("", "clone", "--bare", remote, repoDir)
	return err
}

// Fetch downloads the latest objects from origin into the bare clone at repoDir.
func Fetch(repoDir string) error {
	_, err := run(repoDir, "fetch", "--prune", "origin")
	return err
}

// WorktreeAdd creates a detached worktree at path checked out at sha.
//
// It is idempotent: worktree paths are keyed by SHA, so if a valid worktree
// already exists at path it is already checked out at the right commit and is
// reused as-is. This lets a deploy recover cleanly when a previous attempt was
// interrupted after checkout — for example by a self-update restart of nexus.
// A leftover directory without worktree metadata is cleared before re-creating.
func WorktreeAdd(repoDir, path, sha string) error {
	if isWorktree(path) {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		// Path exists but isn't a valid worktree (e.g. a partially created one).
		// Remove it and prune stale admin entries so `worktree add` can proceed.
		_ = os.RemoveAll(path)
		_, _ = run(repoDir, "worktree", "prune")
	}
	_, err := run(repoDir, "worktree", "add", "--detach", path, sha)
	return err
}

// isWorktree reports whether path is a checked-out git worktree. A linked
// worktree has a .git file (not directory) linking back to the main repo.
func isWorktree(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && !info.IsDir()
}

// WorktreeRemove removes the worktree at path from the bare clone.
func WorktreeRemove(repoDir, path string) error {
	_, err := run(repoDir, "worktree", "remove", "--force", path)
	return err
}

// ResolveRef resolves a nexus ref (@main, @v1.2.3, @latest) to a commit SHA
// by running git ls-remote against origin in the bare clone at repoDir.
func ResolveRef(repoDir, ref string) (string, error) {
	if !strings.HasPrefix(ref, "@") {
		return "", fmt.Errorf("ref %q must start with @", ref)
	}
	name := ref[1:]
	if name == "" {
		return "", fmt.Errorf("ref cannot be empty")
	}
	if name == "latest" {
		out, err := run(repoDir, "ls-remote", "--tags", "--sort=-version:refname", "origin", "refs/tags/*")
		if err != nil {
			return "", fmt.Errorf("ls-remote @latest: %w", err)
		}
		return ParseLsRemoteLatest(out)
	}
	out, err := run(repoDir, "ls-remote", "origin", "refs/heads/"+name, "refs/tags/"+name)
	if err != nil {
		return "", fmt.Errorf("ls-remote %q: %w", ref, err)
	}
	return ParseLsRemoteOutput(out, name)
}

func isBareClone(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil && !info.IsDir()
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
