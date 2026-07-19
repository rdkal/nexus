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

// ResolveRef resolves a nexus ref to a commit SHA by running git ls-remote
// against origin in the bare clone at repoDir. Supported forms:
//
//	main        branch tip (refs/heads/main)
//	v1.2.3      exact tag (refs/tags/v1.2.3), or branch of the same name
//	latest      highest semver tag across all tags
//	<glob>      highest semver tag matching a glob, e.g. web-v*, v2.* —
//	            the user's own tag scheme, matched against refs/tags/<glob>
//
// A ref containing '*' is a tag glob: it never matches a branch. This lets one
// app in a monorepo track only its own tags (whatever prefix it uses) without
// nexus imposing a tag convention.
//
// A leading '@' is accepted but optional — it only has meaning as the separator
// in the "<spec>@<ref>" shorthand, not as a standalone prefix.
func ResolveRef(repoDir, ref string) (string, error) {
	name := strings.TrimPrefix(ref, "@")
	if name == "" {
		return "", fmt.Errorf("ref cannot be empty")
	}
	if name == "latest" || strings.Contains(name, "*") {
		pattern := "refs/tags/*"
		if name != "latest" {
			pattern = "refs/tags/" + name
		}
		out, err := run(repoDir, "ls-remote", "--tags", "--sort=-version:refname", "origin", pattern)
		if err != nil {
			return "", fmt.Errorf("ls-remote %q: %w", ref, err)
		}
		return ParseLsRemoteLatest(out)
	}
	out, err := run(repoDir, "ls-remote", "origin", "refs/heads/"+name, "refs/tags/"+name)
	if err != nil {
		return "", fmt.Errorf("ls-remote %q: %w", ref, err)
	}
	return ParseLsRemoteOutput(out, name)
}

// ResolveRepoRoot discovers the git repository within a spec path by walking up,
// and resolves its transport. It probes candidate remotes from the full path down
// to the shortest and returns the first reachable repo as the repo root — as an
// actual clone URL — with the remaining trailing segments as the in-repo
// subdirectory. So "github.com/org/repo/services/api" yields repo root
// "https://github.com/org/repo" (or whatever transport works) and subdir
// "services/api", exactly as Go resolves a module in a subdirectory. A path that
// is itself a repo resolves on the first prefix with an empty subdir.
//
// For a scheme-less host path each prefix is tried over several transports (see
// candidateRemotes) so it works without the user configuring git insteadOf.
func ResolveRepoRoot(specPath string) (root, subdir string, err error) {
	specPath = strings.TrimRight(specPath, "/")
	if specPath == "" {
		return "", "", fmt.Errorf("spec path cannot be empty")
	}
	segs := strings.Split(specPath, "/")
	for i := len(segs); i >= 1; i-- {
		prefix := strings.Join(segs[:i], "/")
		if prefix == "" {
			continue // skip empty prefixes from a scheme like file://
		}
		for _, remote := range candidateRemotes(prefix) {
			if remoteExists(remote) {
				return remote, strings.Join(segs[i:], "/"), nil
			}
		}
	}
	return "", "", fmt.Errorf("no git repository found for spec path %q", specPath)
}

// candidateRemotes returns the transports to try for a spec, in order. A spec
// that already carries a scheme (https://…, file://…) or is scp-like (git@host:…)
// is used verbatim. A scheme-less host path (github.com/org/repo) is tried:
//  1. as-is — so a git `insteadOf` the user configured still wins;
//  2. over HTTPS — the common no-config case;
//  3. over SSH (git@host:path).
func candidateRemotes(spec string) []string {
	if strings.Contains(spec, "://") || strings.Contains(spec, "@") {
		return []string{spec}
	}
	cands := []string{spec, "https://" + spec}
	if host, path, ok := strings.Cut(spec, "/"); ok && strings.Contains(host, ".") {
		cands = append(cands, "git@"+host+":"+path)
	}
	return cands
}

// remoteExists reports whether remote is a reachable git repository. Credential
// and SSH prompts are disabled so a probe of a non-existent path fails fast
// instead of hanging.
func remoteExists(remote string) bool {
	cmd := exec.Command("git", "ls-remote", "--quiet", remote, "HEAD")
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes",
	)
	return cmd.Run() == nil
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
