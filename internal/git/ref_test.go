package git_test

import (
	"testing"

	"github.com/rdkal/nexus/internal/git"
)

func TestParseLsRemoteOutput_Branch(t *testing.T) {
	out := "abc123\trefs/heads/main\n"
	sha, err := git.ParseLsRemoteOutput(out, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "abc123" {
		t.Errorf("got %q, want abc123", sha)
	}
}

func TestParseLsRemoteOutput_Tag(t *testing.T) {
	out := "def456\trefs/tags/v1.2.3\n"
	sha, err := git.ParseLsRemoteOutput(out, "v1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "def456" {
		t.Errorf("got %q, want def456", sha)
	}
}

func TestParseLsRemoteOutput_PrefersBranchOverTag(t *testing.T) {
	out := "branch-sha\trefs/heads/dev\ntag-sha\trefs/tags/dev\n"
	sha, err := git.ParseLsRemoteOutput(out, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "branch-sha" {
		t.Errorf("got %q (want branch-sha)", sha)
	}
}

func TestParseLsRemoteOutput_NotFound(t *testing.T) {
	out := "abc123\trefs/heads/main\n"
	if _, err := git.ParseLsRemoteOutput(out, "missing"); err == nil {
		t.Error("expected error for missing ref")
	}
}

func TestParseLsRemoteOutput_Empty(t *testing.T) {
	if _, err := git.ParseLsRemoteOutput("", "main"); err == nil {
		t.Error("expected error for empty output")
	}
}

func TestParseLsRemoteLatest(t *testing.T) {
	// Highest version first (sorted by git --sort=-version:refname)
	out := "sha3\trefs/tags/v1.3.0\nsha2\trefs/tags/v1.2.0\nsha1\trefs/tags/v1.0.0\n"
	sha, err := git.ParseLsRemoteLatest(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "sha3" {
		t.Errorf("got %q, want sha3", sha)
	}
}

func TestParseLsRemoteLatest_PrefersPeeledCommit(t *testing.T) {
	// Annotated tags emit two lines: the tag-object SHA and the ^{} peeled commit
	// SHA. We must resolve to a commit (git worktree add rejects a tag object),
	// so the peeled line wins regardless of which line comes first.
	out := "tag-sha\trefs/tags/v1.0.0\ncommit-sha\trefs/tags/v1.0.0^{}\n"
	sha, err := git.ParseLsRemoteLatest(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "commit-sha" {
		t.Errorf("got %q, want commit-sha", sha)
	}
}

func TestParseLsRemoteLatest_PrefersPeeledCommit_PeeledLineFirst(t *testing.T) {
	// Real `git ls-remote --sort=-version:refname` output orders the peeled
	// line before the plain tag line (unlike the fixture above) — must not
	// depend on line order.
	out := "commit-sha\trefs/tags/v1.0.0^{}\ntag-sha\trefs/tags/v1.0.0\n"
	sha, err := git.ParseLsRemoteLatest(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "commit-sha" {
		t.Errorf("got %q, want commit-sha", sha)
	}
}

func TestParseLsRemoteLatest_LightweightTag(t *testing.T) {
	// A lightweight tag has no ^{} peeled line; its plain SHA is already a commit.
	out := "commit-sha\trefs/tags/v1.0.0\n"
	sha, err := git.ParseLsRemoteLatest(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "commit-sha" {
		t.Errorf("got %q, want commit-sha", sha)
	}
}

func TestParseLsRemoteLatest_Empty(t *testing.T) {
	if _, err := git.ParseLsRemoteLatest(""); err == nil {
		t.Error("expected error for empty output")
	}
}
