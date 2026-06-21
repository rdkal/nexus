package git

import (
	"fmt"
	"strings"
)

// ParseLsRemoteOutput extracts the SHA for name from ls-remote output.
// It prefers refs/heads/<name> over refs/tags/<name> when both appear.
// output is the raw stdout of:
//
//	git ls-remote <remote> refs/heads/<name> refs/tags/<name>
func ParseLsRemoteOutput(output, name string) (string, error) {
	headsRef := "refs/heads/" + name
	tagsRef := "refs/tags/" + name
	var tagSHA string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		sha, ref, ok := parseLine(line)
		if !ok {
			continue
		}
		switch ref {
		case headsRef:
			return sha, nil // branch wins immediately
		case tagsRef:
			tagSHA = sha
		}
	}
	if tagSHA != "" {
		return tagSHA, nil
	}
	return "", fmt.Errorf("ref %q not found", name)
}

// ParseLsRemoteLatest returns the SHA of the highest semver tag from
// ls-remote --tags --sort=-version:refname output (first line wins).
// Peeled tag lines (ending with ^{}) are skipped.
func ParseLsRemoteLatest(output string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" || strings.HasSuffix(line, "^{}") {
			continue
		}
		sha, ref, ok := parseLine(line)
		if !ok {
			continue
		}
		if strings.HasPrefix(ref, "refs/tags/") {
			return sha, nil
		}
	}
	return "", fmt.Errorf("no semver tags found")
}

func parseLine(line string) (sha, ref string, ok bool) {
	parts := strings.SplitN(line, "\t", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
