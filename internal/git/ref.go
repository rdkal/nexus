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

// ParseLsRemoteLatest returns the commit SHA of the highest semver tag from
// ls-remote --tags --sort=-version:refname output (the first tag name wins).
//
// An annotated tag emits two lines for the same ref name: the tag *object*
// SHA (refs/tags/<name>) and the peeled commit SHA (refs/tags/<name>^{}) it
// points to — order between the two is not guaranteed. A lightweight tag
// emits only the plain line. We must resolve to a commit, so the peeled SHA
// wins whenever both are present; the plain SHA is only a commit for a
// lightweight tag (used as a fallback when no ^{} line is seen).
func ParseLsRemoteLatest(output string) (string, error) {
	var bestRef, plainSHA, peeledSHA string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		sha, ref, ok := parseLine(line)
		if !ok || !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		peeled := strings.HasSuffix(ref, "^{}")
		baseRef := strings.TrimSuffix(ref, "^{}")
		if bestRef == "" {
			bestRef = baseRef
		} else if baseRef != bestRef {
			break // moved on to a lower-sorted tag; the highest one's lines are exhausted
		}
		if peeled {
			peeledSHA = sha
		} else {
			plainSHA = sha
		}
	}
	if peeledSHA != "" {
		return peeledSHA, nil
	}
	if plainSHA != "" {
		return plainSHA, nil
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
