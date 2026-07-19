package git

import (
	"reflect"
	"testing"
)

func TestCandidateRemotes(t *testing.T) {
	cases := []struct {
		spec string
		want []string
	}{
		// Scheme-less host path: as-is, https, ssh.
		{"github.com/org/repo", []string{
			"github.com/org/repo",
			"https://github.com/org/repo",
			"git@github.com:org/repo",
		}},
		// Already has a scheme → verbatim.
		{"https://github.com/org/repo", []string{"https://github.com/org/repo"}},
		{"file:///tmp/x/repo.git", []string{"file:///tmp/x/repo.git"}},
		// scp-like ssh form → verbatim.
		{"git@github.com:org/repo", []string{"git@github.com:org/repo"}},
		// No dot in first segment (not a host) → no ssh candidate.
		{"local/repo", []string{"local/repo", "https://local/repo"}},
	}
	for _, c := range cases {
		got := candidateRemotes(c.spec)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("candidateRemotes(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}
