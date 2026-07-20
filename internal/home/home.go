package home

import (
	"fmt"
	"os"
	"path/filepath"
)

const envKey = "NEXUS_HOME"

// Dir returns the NEXUS_HOME path: env var if set, otherwise ~/.nexus.
func Dir() (string, error) {
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	hd, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(hd, ".nexus"), nil
}

// Setup creates the standard NEXUS_HOME directory structure. Idempotent.
func Setup(dir string) error {
	for _, sub := range []string{"bin", "repos", "volumes", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return fmt.Errorf("create %s/%s: %w", dir, sub, err)
		}
	}
	return nil
}

// Paths holds the well-known paths within a NEXUS_HOME directory.
type Paths struct {
	Home     string
	Bin      string
	Repos    string
	Volumes  string
	Logs     string
	DB       string
	Socket   string // nexus runtime API socket (nexus.sock)
	PMSocket string // nexus-pm process manager API socket (nexus-pm.sock)
}

// MaxSocketPath is the largest Unix-domain socket path length that binds
// reliably across platforms. The kernel copies the path into
// sockaddr_un.sun_path — 108 bytes on Linux, 104 on macOS/BSD, including the
// trailing NUL — so we use the smaller platform's usable length. Beyond it,
// bind(2) fails with a bare "invalid argument".
const MaxSocketPath = 103

// CheckSocketPath returns an actionable error if path is too long to bind as a
// Unix-domain socket. Callers should check before net.Listen so an over-long
// NEXUS_HOME is explained rather than surfacing the kernel's "invalid argument".
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf(
			"socket path %q is %d bytes, over the %d-byte limit the OS allows for Unix sockets; "+
				"set a shorter NEXUS_HOME (the default ~/.nexus is short enough)",
			path, len(path), MaxSocketPath)
	}
	return nil
}

// NewPaths constructs Paths rooted at the given NEXUS_HOME directory.
func NewPaths(home string) Paths {
	return Paths{
		Home:     home,
		Bin:      filepath.Join(home, "bin"),
		Repos:    filepath.Join(home, "repos"),
		Volumes:  filepath.Join(home, "volumes"),
		Logs:     filepath.Join(home, "logs"),
		DB:       filepath.Join(home, "nexus.db"),
		Socket:   filepath.Join(home, "nexus.sock"),
		PMSocket: filepath.Join(home, "nexus-pm.sock"),
	}
}

// VolumeDir returns the filesystem path for a named volume at the given resource address.
// e.g. VolumeDir("my-system/db", "data") → <home>/volumes/my-system/db/data
func (p Paths) VolumeDir(address, volume string) string {
	return filepath.Join(p.Volumes, filepath.FromSlash(address), volume)
}

// LogDir returns the log directory for a resource address.
func (p Paths) LogDir(address string) string {
	return filepath.Join(p.Logs, filepath.FromSlash(address))
}

// BuildLog returns the build log path for a deployment SHA.
// e.g. BuildLog("my-system/db", "abc123") → <home>/logs/my-system/db/abc123-build.log
func (p Paths) BuildLog(address, sha string) string {
	return filepath.Join(p.LogDir(address), sha+"-build.log")
}

// ServiceLog returns the rolling log path for a service.
// e.g. ServiceLog("my-system/db/postgres") → <home>/logs/my-system/db/postgres/current.log
func (p Paths) ServiceLog(address string) string {
	return filepath.Join(p.LogDir(address), "current.log")
}

// RepoDir returns the bare clone directory for a spec path.
// e.g. RepoDir("github.com/myorg/api") → <home>/repos/github.com/myorg/api
func (p Paths) RepoDir(specPath string) string {
	return filepath.Join(p.Repos, filepath.FromSlash(specPath))
}

// WorktreeDir returns the worktree directory for a project instance.
// rootSpecPath is the spec path of the root deployment.
// aliases is the chain of project aliases from root to this project.
// e.g. WorktreeDir("github.com/myorg/my-system", []string{"db"}, "abc123")
//
//	→ <home>/repos/github.com/myorg/my-system/db/worktrees/abc123
func (p Paths) WorktreeDir(rootSpecPath string, aliases []string, sha string) string {
	base := filepath.Join(p.Repos, filepath.FromSlash(rootSpecPath))
	for _, alias := range aliases {
		base = filepath.Join(base, alias)
	}
	return filepath.Join(base, "worktrees", sha)
}
