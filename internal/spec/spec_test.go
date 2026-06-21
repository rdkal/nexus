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
		wantName string
		wantErr  bool
	}{
		{"github.com/myorg/api", "github.com/myorg/api", "api", false},
		{"github.com/myorg/api:my-api", "github.com/myorg/api", "my-api", false},
		{"github.com/myorg/monorepo/services/api:api-svc", "github.com/myorg/monorepo/services/api", "api-svc", false},
		{"", "", "", true},
	}
	for _, c := range cases {
		path, name, err := spec.ParseAddArg(c.arg)
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
		if path != c.wantPath || name != c.wantName {
			t.Errorf("ParseAddArg(%q) = (%q, %q), want (%q, %q)", c.arg, path, name, c.wantPath, c.wantName)
		}
	}
}
