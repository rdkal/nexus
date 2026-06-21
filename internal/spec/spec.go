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

// ParseAddArg parses a "spec-path[:name]" argument used with nexus project add.
// The name defaults to the final segment of the spec path.
func ParseAddArg(arg string) (path Path, name string, err error) {
	parts := strings.SplitN(arg, ":", 2)
	path = Path(parts[0])
	if path == "" {
		return "", "", fmt.Errorf("spec path cannot be empty")
	}
	if len(parts) == 2 && parts[1] != "" {
		name = parts[1]
	} else {
		name = path.ProjectName()
	}
	if name == "" {
		return "", "", fmt.Errorf("could not infer project name from %q", arg)
	}
	return path, name, nil
}
