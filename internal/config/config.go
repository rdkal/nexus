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
	Tasks       map[string]Task       `yaml:"tasks"`
	Projects    map[string]SubProject `yaml:"projects"`
}

// Service is a named long-running process.
type Service struct {
	Run         string `yaml:"run"`
	Environment Env    `yaml:"environment"`
}

// Task is a one-shot command run on a trigger: a cron schedule, another task's
// success (After), or manually. See DESIGN "Tasks (scheduled & triggered)".
type Task struct {
	Run         string    `yaml:"run"`
	Schedule    string    `yaml:"schedule"` // cron / "@every 15m" / "@daily" — a time trigger
	After       AfterList `yaml:"after"`    // sibling task(s) — run when they succeed (all, for a join)
	Environment Env       `yaml:"environment"`
}

// AfterList is a task's after: trigger. A single task name runs the task when
// that one succeeds; a list is a fan-in join — the task runs when all of the
// named tasks have succeeded. Accepts either YAML form:
//
//	after: build          # single parent
//	after: [test, lint]   # join: run when both succeed
type AfterList []string

func (a *AfterList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		if s == "" {
			*a = nil
		} else {
			*a = AfterList{s}
		}
		return nil
	case yaml.SequenceNode:
		var xs []string
		if err := node.Decode(&xs); err != nil {
			return err
		}
		*a = AfterList(xs)
		return nil
	default:
		return fmt.Errorf("after: must be a task name or a list of task names")
	}
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
			k, v, ok := strings.Cut(item, "=")
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if ok {
				out[k] = v
			} else {
				// A bare "KEY" (no =) forwards the daemon's value, docker-compose style.
				out[k] = "${" + k + "}"
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

	// Build/Volumes/Services/Projects are the inline definition — ignored for
	// external projects, whose definition comes from the remote nexus.yaml.
	Build    string                `yaml:"build"`
	Volumes  map[string]struct{}   `yaml:"volumes"`
	Services map[string]Service    `yaml:"services"`
	Projects map[string]SubProject `yaml:"projects"`

	// Environment applies in BOTH cases: for an inline project it is that unit's
	// environment:, and for an external project it is the composer's override,
	// injected into the child's build and services.
	Environment Env `yaml:"environment"`
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

// ValidateTasks checks a project's tasks: each has a run command and at most one
// trigger, every after: names a real sibling task, and the after: graph has no
// cycle. Returns the first problem found (deploy fails loudly, like an undefined
// ${VAR}).
func ValidateTasks(tasks map[string]Task) error {
	for name, t := range tasks {
		if t.Run == "" {
			return fmt.Errorf("task %q: missing run", name)
		}
		if t.Schedule != "" && len(t.After) > 0 {
			return fmt.Errorf("task %q: set only one of schedule or after", name)
		}
		seen := map[string]bool{}
		for _, p := range t.After {
			switch {
			case p == "":
				return fmt.Errorf("task %q: after has an empty task name", name)
			case p == name:
				return fmt.Errorf("task %q: after references itself", name)
			case seen[p]:
				return fmt.Errorf("task %q: after lists %q twice", name, p)
			}
			seen[p] = true
			if _, ok := tasks[p]; !ok {
				return fmt.Errorf("task %q: after references unknown task %q", name, p)
			}
		}
	}
	// Detect a cycle in the after graph. A task may now have several parents (a
	// join), so it is a general DAG — walk it with a depth-first search, colouring
	// each node unvisited / on-stack / done; a back-edge to an on-stack node is a
	// cycle.
	const onStack, done = 1, 2
	state := map[string]int{}
	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case onStack:
			return fmt.Errorf("task %q: after: forms a cycle", n)
		case done:
			return nil
		}
		state[n] = onStack
		for _, p := range tasks[n].After {
			if err := visit(p); err != nil {
				return err
			}
		}
		state[n] = done
		return nil
	}
	// Visit in a stable order so the reported cycle is deterministic.
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
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
	// Environment is the environment: set on this entry in the parent's projects:
	// map — the composer configuring/overriding the reusable sub-project. It is
	// injected into the child's build and services (see penv), overriding the
	// child's own committed environment:.
	Environment map[string]string
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
			*external = append(*external, ExternalRef{RelPath: rel, Src: sub.Src, Ref: sub.Ref, Environment: sub.Environment})
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
