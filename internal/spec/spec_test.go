package spec_test

import (
	"testing"

	"github.com/rdkal/nexus/internal/spec"
)

func TestProjectName(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"github.com/myorg/my-system", "my-system"},
		{"github.com/nexus-community/postgres", "postgres"},
		{"github.com/myorg/monorepo/services/api", "api"},
		{"single", "single"},
	}
	for _, c := range cases {
		got := spec.Path(c.path).ProjectName()
		if got != c.want {
			t.Errorf("Path(%q).ProjectName() = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestParseAddArg(t *testing.T) {
	cases := []struct {
		arg      string
		wantPath spec.Path
		wantRef  string
		wantName string
		wantErr  bool
	}{
		{"github.com/myorg/api", "github.com/myorg/api", "", "api", false},
		{"github.com/myorg/api:my-api", "github.com/myorg/api", "", "my-api", false},
		{"github.com/myorg/monorepo/services/api:api-svc", "github.com/myorg/monorepo/services/api", "", "api-svc", false},
		// @ref shorthand
		{"github.com/myorg/api@v15", "github.com/myorg/api", "v15", "api", false},
		{"github.com/myorg/api@web-v*", "github.com/myorg/api", "web-v*", "api", false},
		// ref + name together
		{"github.com/myorg/api@v15:my-api", "github.com/myorg/api", "v15", "my-api", false},
		{"", "", "", "", true},
	}
	for _, c := range cases {
		path, ref, name, err := spec.ParseAddArg(c.arg)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseAddArg(%q): expected error, got none", c.arg)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAddArg(%q): unexpected error: %v", c.arg, err)
			continue
		}
		if path != c.wantPath || ref != c.wantRef || name != c.wantName {
			t.Errorf("ParseAddArg(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.arg, path, ref, name, c.wantPath, c.wantRef, c.wantName)
		}
	}
}
