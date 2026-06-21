package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ProjectFile is the parsed content of a nexus.yaml.
type ProjectFile struct {
	Build    string                `yaml:"build"`
	Volumes  map[string]struct{}   `yaml:"volumes"`
	Services map[string]Service    `yaml:"services"`
	Projects map[string]SubProject `yaml:"projects"`
}

// Service is a named long-running process.
type Service struct {
	Run string `yaml:"run"`
}

// SubProject is an entry in the projects: map.
// External projects have Src set; inline projects do not.
type SubProject struct {
	// External-only fields.
	Src string `yaml:"src"`
	Ref string `yaml:"ref"`

	// Inline fields — ignored for external projects (they come from the remote nexus.yaml).
	Build    string                `yaml:"build"`
	Volumes  map[string]struct{}   `yaml:"volumes"`
	Services map[string]Service    `yaml:"services"`
	Projects map[string]SubProject `yaml:"projects"`
}

// IsExternal reports whether this sub-project references an external git repo.
func (s SubProject) IsExternal() bool { return s.Src != "" }

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
