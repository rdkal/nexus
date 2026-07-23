package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProjectFile is the parsed content of a nexus.yaml.
type ProjectFile struct {
	Build       string                `yaml:"build"`
	Environment Env                   `yaml:"environment"`
	Volumes     map[string]struct{}   `yaml:"volumes"`
	Services    map[string]Service    `yaml:"services"`
	Projects    map[string]SubProject `yaml:"projects"`
}

// Service is a named long-running process.
type Service struct {
	Run         string `yaml:"run"`
	Environment Env    `yaml:"environment"`
}

// Env is a set of environment variables. It accepts both docker-compose forms:
//
//	environment:
//	  KEY: value
//	  PORT: 8080
//
//	environment:
//	  - KEY=value
//	  - PORT=8080
//
// Values are always strings (a bare number/bool is stringified).
type Env map[string]string

// UnmarshalYAML decodes the map form or the list ("KEY=value") form.
func (e *Env) UnmarshalYAML(node *yaml.Node) error {
	out := map[string]string{}
	switch node.Kind {
	case yaml.MappingNode:
		var m map[string]any
		if err := node.Decode(&m); err != nil {
			return err
		}
		for k, v := range m {
			if v == nil {
				out[k] = ""
			} else {
				out[k] = fmt.Sprint(v)
			}
		}
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return err
		}
		for _, item := range list {
			k, v, _ := strings.Cut(item, "=")
			if k = strings.TrimSpace(k); k != "" {
				out[k] = v
			}
		}
	default:
		return fmt.Errorf("environment must be a map or a list, got %v", node.Kind)
	}
	*e = out
	return nil
}

// SubProject is an entry in the projects: map.
// External projects have Src set; inline projects do not.
type SubProject struct {
	// External-only fields.
	Src string `yaml:"src"`
	Ref string `yaml:"ref"`

	// Inline fields — ignored for external projects (they come from the remote nexus.yaml).
	Build       string                `yaml:"build"`
	Environment Env                   `yaml:"environment"`
	Volumes     map[string]struct{}   `yaml:"volumes"`
	Services    map[string]Service    `yaml:"services"`
	Projects    map[string]SubProject `yaml:"projects"`
}

// IsExternal reports whether this sub-project references an external git repo.
func (s SubProject) IsExternal() bool { return s.Src != "" }

// UnmarshalYAML accepts a projects: entry in either form:
//
//	db: github.com/community/postgres@v15   # string shorthand: <spec>[@<ref>]
//	db: { src: github.com/community/postgres, ref: v15 }   # map (external)
//	metrics: { services: { ... } }                          # map (inline)
//
// The string shorthand is always an external sub-project; the '@' separates the
// spec path from the ref (a bare spec uses the default ref).
func (s *SubProject) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		spec, ref, _ := strings.Cut(node.Value, "@")
		s.Src = spec
		s.Ref = ref
		return nil
	}
	// Map form. The alias type has no UnmarshalYAML, so Decode fills the fields
	// directly instead of recursing.
	type rawSubProject SubProject
	var raw rawSubProject
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*s = SubProject(raw)
	return nil
}

// Parse reads and parses a nexus.yaml file at the given path.
func Parse(path string) (*ProjectFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f ProjectFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &f, nil
}

// ParseBytes parses nexus.yaml content from a byte slice.
func ParseBytes(data []byte) (*ProjectFile, error) {
	var f ProjectFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse nexus.yaml: %w", err)
	}
	return &f, nil
}

// InlineUnit is one project within an inline subtree: the base project plus every
// inline (src-less) descendant. All units in a subtree deploy atomically and share
// one worktree. RelPath is the alias chain from the base project (nil = the base
// project itself).
type InlineUnit struct {
	RelPath     []string
	Build       string
	Environment map[string]string
	Volumes     map[string]struct{}
	Services    map[string]Service
}

// ExternalRef is an external sub-project (has src:) discovered while flattening an
// inline subtree. RelPath is the alias chain from the base project. External
// sub-projects deploy independently, so flattening does not recurse into them.
type ExternalRef struct {
	RelPath []string
	Src     string
	Ref     string
}

// Flatten walks the inline subtree of a project file. It returns the inline units
// to deploy atomically with this project (the base plus every src-less descendant),
// and the external sub-projects to deploy independently. Both slices are sorted by
// their joined RelPath for deterministic ordering; the base unit sorts first.
func (f *ProjectFile) Flatten() (units []InlineUnit, external []ExternalRef) {
	units = append(units, InlineUnit{
		Build:       f.Build,
		Environment: f.Environment,
		Volumes:     f.Volumes,
		Services:    f.Services,
	})
	flattenProjects(f.Projects, nil, &units, &external)

	sort.SliceStable(units, func(i, j int) bool {
		return joinRel(units[i].RelPath) < joinRel(units[j].RelPath)
	})
	sort.SliceStable(external, func(i, j int) bool {
		return joinRel(external[i].RelPath) < joinRel(external[j].RelPath)
	})
	return units, external
}

func flattenProjects(projects map[string]SubProject, prefix []string, units *[]InlineUnit, external *[]ExternalRef) {
	for alias, sub := range projects {
		rel := append(append([]string{}, prefix...), alias)
		if sub.IsExternal() {
			*external = append(*external, ExternalRef{RelPath: rel, Src: sub.Src, Ref: sub.Ref})
			continue
		}
		*units = append(*units, InlineUnit{
			RelPath:     rel,
			Build:       sub.Build,
			Environment: sub.Environment,
			Volumes:     sub.Volumes,
			Services:    sub.Services,
		})
		flattenProjects(sub.Projects, rel, units, external)
	}
}

func joinRel(rel []string) string { return strings.Join(rel, "/") }
