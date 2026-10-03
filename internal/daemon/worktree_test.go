package daemon

import "testing"

// worktreeInUse is what stops one project's deploy from deleting another's checkout.
// Worktree paths are keyed by root spec path, alias chain and SHA rather than by
// project, so two root projects of one repo share a path whenever they are on the
// same SHA.
func TestWorktreeInUse(t *testing.T) {
	d := &Daemon{projects: map[string]*projectState{}}
	shared := "/home/u/.nexus/repos/github.com/org/two-apps/worktrees/aaa"
	other := "/home/u/.nexus/repos/github.com/org/two-apps/worktrees/bbb"

	d.projects["recorder"] = &projectState{address: "recorder", worktree: shared}
	d.projects["trader"] = &projectState{address: "trader", worktree: shared}
	d.projects["elsewhere"] = &projectState{address: "elsewhere", worktree: other}
	d.projects["never-deployed"] = &projectState{address: "never-deployed"}

	for _, c := range []struct {
		name, path, except string
		want               bool
	}{
		{"shared path, asked by one of the two", shared, "recorder", true},
		{"shared path, asked by the other", shared, "trader", true},
		{"a project's own worktree, nobody else on it", other, "elsewhere", false},
		{"asked by a third party", other, "recorder", true},
		{"a path nothing is deployed from", "/home/u/.nexus/repos/x/worktrees/zzz", "recorder", false},
		{"empty path is never in use", "", "recorder", false},
	} {
		if got := d.worktreeInUse(c.path, c.except); got != c.want {
			t.Errorf("%s: worktreeInUse(%q, %q) = %v, want %v", c.name, c.path, c.except, got, c.want)
		}
	}
}
