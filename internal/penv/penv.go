// Package penv builds the environment for a project's build and service
// processes: the daemon's own environment, the NEXUS_* contract variables,
// volume paths (the unit's own plus every known project's, for cross-project
// wiring), an optional .env file, and docker-compose-style environment: maps
// with ${VAR} interpolation.
package penv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rdkal/nexus/internal/home"
)

// Input describes everything needed to build one unit's process environment.
type Input struct {
	Paths    home.Paths
	Address  string // unit resource address → NEXUS_PROJECT
	Ref      string // → NEXUS_REF
	SHA      string // → NEXUS_SHA
	WorkDir  string // app dir → NEXUS_WORKTREE, and where .env is read from

	// OwnVolumes are this unit's declared volumes, exposed as NEXUS_VOLUME_<NAME>.
	OwnVolumes map[string]struct{}
	// GlobalVolumes maps NEXUS_<PROJECT>_<VOLUME> variable names to absolute paths,
	// covering every project on the instance so one project can reference another's
	// volume (e.g. NEXUS_TRAEFIK_DYNAMIC) without hardcoding a path.
	GlobalVolumes map[string]string

	// ProjectEnv is the project-level environment: map; ServiceEnv is a service's
	// own environment: map (nil for the build step). ServiceEnv wins over
	// ProjectEnv, which wins over .env, which wins over the inherited environment.
	// Values may reference other variables as ${VAR} or $VAR ($$ is a literal $).
	ProjectEnv map[string]string
	ServiceEnv map[string]string

	// ParentEnv is the environment: a parent set on this project's entry in its
	// projects: map (the composer configuring a nested sub-project). It overrides
	// the project's own ProjectEnv/ServiceEnv, but not the operator .env. Empty
	// for a root project.
	ParentEnv map[string]string
}

// passthrough is the allowlist of daemon environment variables a project's
// processes inherit — the essentials for finding binaries, the home directory,
// and locale. Every *other* daemon variable is withheld, so a secret set for one
// project is not visible to another: a project sees only what it declares (plus
// the NEXUS_* contract). A specific daemon variable can still be forwarded
// on purpose via interpolation, e.g. environment: { TOKEN: ${DAEMON_TOKEN} }.
var passthrough = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL",
	"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE",
	"TERM", "TZ", "TMPDIR",
}

// Build assembles the final environment slice (KEY=VALUE entries, sorted). It
// returns an error if an environment: value references a variable that is not
// defined anywhere — a typo, or a secret the operator forgot to set — rather than
// silently expanding it to empty.
func Build(in Input) ([]string, error) {
	host := envMap(os.Environ())

	// Layer 1: only the allowlisted essentials from the daemon environment.
	base := make(map[string]string, len(passthrough))
	for _, k := range passthrough {
		if v, ok := host[k]; ok {
			base[k] = v
		}
	}

	// Layer 2: the NEXUS_* contract. Kept authoritative — user env cannot shadow it.
	nx := map[string]string{
		"NEXUS_HOME":     in.Paths.Home,
		"NEXUS_PROJECT":  in.Address,
		"NEXUS_SHA":      in.SHA,
		"NEXUS_REF":      in.Ref,
		"NEXUS_WORKTREE": in.WorkDir,
	}
	for name := range in.OwnVolumes {
		p := in.Paths.VolumeDir(in.Address, name)
		_ = os.MkdirAll(p, 0o755)
		nx["NEXUS_VOLUME_"+envToken(name)] = p
	}
	for k, v := range in.GlobalVolumes {
		nx[k] = v
	}

	// Layer 3: two .env files — the repo's (committed defaults, next to nexus.yaml)
	// and the operator's (<home>/env/<address>.env, not in git, host-specific
	// secrets/overrides). The operator file wins.
	repoEnv := readDotenv(filepath.Join(in.WorkDir, ".env"))
	homeEnv := readDotenv(in.Paths.EnvFile(in.Address))

	// A reference resolves if the name is defined anywhere below (including the
	// full daemon env, for opt-in forwarding, and the declared keys themselves).
	defined := make(map[string]string)
	for _, m := range []map[string]string{host, nx, repoEnv, homeEnv, in.ProjectEnv, in.ServiceEnv, in.ParentEnv} {
		for k := range m {
			defined[k] = ""
		}
	}

	// Interpolation may reference any daemon variable by name (opt-in forwarding),
	// plus everything nexus provides and both .env files — but the full host
	// environment is only a *lookup* source, never copied into the result.
	missing := make(map[string]string)
	lookup := merge(host, nx, repoEnv, homeEnv)
	proj := interpolateAll(in.ProjectEnv, lookup, defined, missing)
	lookup = merge(lookup, proj)
	svc := interpolateAll(in.ServiceEnv, lookup, defined, missing)
	lookup = merge(lookup, svc)
	parent := interpolateAll(in.ParentEnv, lookup, defined, missing)

	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"environment references undefined variable(s): %s — define them in environment:, the repo .env, or %s",
			strings.Join(sortedKeys(missing), ", "), in.Paths.EnvFile(in.Address))
	}

	// Final precedence, last wins: essentials < repo .env < project < service <
	// parent (composer) < operator .env < NEXUS_*.
	final := merge(base, repoEnv, proj, svc, parent, homeEnv, nx)
	return sortedEnv(final), nil
}

// VolumeVar returns the global environment variable name for a project's volume,
// e.g. VolumeVar("traefik", "dynamic") == "NEXUS_TRAEFIK_DYNAMIC" and
// VolumeVar("my-system/db", "data") == "NEXUS_MY_SYSTEM_DB_DATA".
func VolumeVar(address, volume string) string {
	return "NEXUS_" + envToken(address) + "_" + envToken(volume)
}

// envToken upper-cases a name and turns path/dash separators into underscores so
// it is a valid environment variable identifier.
func envToken(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return strings.ToUpper(s)
}

func envMap(entries []string) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

func merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func sortedEnv(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func interpolateAll(in, lookup, defined, missing map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = interpolate(v, lookup, defined, missing)
	}
	return out
}

// interpolate expands variable references from lookup. Supported forms, matching
// docker compose:
//
//	$NAME, ${NAME}          the variable (error if defined nowhere)
//	${NAME:-default}        NAME if set and non-empty, else default
//	${NAME-default}         NAME if set (even empty), else default
//	$$                      a literal $
//
// A reference with a default never errors. A plain reference whose name is absent
// from defined is recorded in missing (defined-but-empty is fine); the caller
// turns a non-empty missing set into an error.
func interpolate(s string, lookup, defined, missing map[string]string) string {
	if !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' { // $$ → $
			b.WriteByte('$')
			i += 2
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' { // ${...}
			if end := matchBrace(s, i+1); end >= 0 {
				b.WriteString(resolveBrace(s[i+2:end], lookup, defined, missing))
				i = end + 1
				continue
			}
		}
		// $NAME
		j := i + 1
		for j < len(s) && isNameChar(s[j]) {
			j++
		}
		if j > i+1 {
			b.WriteString(resolvePlain(s[i+1:j], lookup, defined, missing))
			i = j
			continue
		}
		b.WriteByte('$') // lone $
		i++
	}
	return b.String()
}

// matchBrace returns the index of the '}' closing the '{' at open, honoring
// nested ${...}, or -1 if unmatched.
func matchBrace(s string, open int) int {
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return j
			}
		}
	}
	return -1
}

// resolvePlain resolves a bare NAME, recording it as missing if undefined.
func resolvePlain(name string, lookup, defined, missing map[string]string) string {
	if _, ok := defined[name]; !ok {
		missing[name] = ""
	}
	return lookup[name]
}

// resolveBrace resolves the contents of a ${...}, handling :-  and - defaults.
// The default is evaluated (and its own references checked) only when actually
// used, so ${SET:-${OTHER}} never faults on OTHER when SET is present.
func resolveBrace(expr string, lookup, defined, missing map[string]string) string {
	if idx := strings.Index(expr, ":-"); idx >= 0 {
		name := expr[:idx]
		if v := lookup[name]; v != "" {
			return v
		}
		return interpolate(expr[idx+2:], lookup, defined, missing)
	}
	if idx := strings.IndexByte(expr, '-'); idx >= 0 {
		name := expr[:idx]
		if v, ok := lookup[name]; ok {
			return v
		}
		return interpolate(expr[idx+1:], lookup, defined, missing)
	}
	return resolvePlain(expr, lookup, defined, missing)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isNameChar(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// readDotenv parses a simple KEY=VALUE .env file. Blank lines and #-comments are
// skipped, an optional leading "export " is ignored, and matching single or
// double quotes around a value are stripped. Missing file → empty map.
func readDotenv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if k != "" {
			out[k] = v
		}
	}
	return out
}
