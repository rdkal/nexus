package spec

import (
	"fmt"
	"strings"
)

// Path is a spec path like "github.com/myorg/my-system" — no scheme.
type Path string

// ProjectName returns the default project name: the final path segment.
func (p Path) ProjectName() string {
	s := string(p)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ParseAddArg parses a "spec-path[@ref][:name]" argument used with nexus project
// add. The '@ref' shorthand and the ':name' override are both optional; ref is
// empty when not given (the caller applies its default), and name defaults to
// the final segment of the spec path.
func ParseAddArg(arg string) (path Path, ref, name string, err error) {
	rest := arg
	// :name — the only ':' in a scheme-less spec path is the name separator.
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		name = rest[i+1:]
		rest = rest[:i]
	}
	// @ref — separates the spec path from the ref.
	if i := strings.Index(rest, "@"); i >= 0 {
		ref = rest[i+1:]
		rest = rest[:i]
	}
	path = Path(rest)
	if path == "" {
		return "", "", "", fmt.Errorf("spec path cannot be empty")
	}
	if name == "" {
		name = path.ProjectName()
	}
	if name == "" {
		return "", "", "", fmt.Errorf("could not infer project name from %q", arg)
	}
	return path, ref, name, nil
}
