#!/bin/sh
# Nexus installer.
#
# Usage:
#   curl -fsSL https://github.com/rdkal/nexus/raw/main/install.sh | sh
#
# What it does:
#   1. Downloads prebuilt nexus-pm and nexus into $NEXUS_HOME/bin
#   2. Creates the $NEXUS_HOME directory structure
#   3. Installs and starts a user-mode service pointing at nexus-pm
#
# Then add projects with `nexus project add <spec-path>`.
#
# Requirements: git and curl on PATH. No Go toolchain, no root.

set -eu

NEXUS_REPO_URL="https://github.com/rdkal/nexus"

# --- configuration (overridable via env or flags) ---
NEXUS_HOME="${NEXUS_HOME:-$HOME/.nexus}"
NEXUS_REF="${NEXUS_REF:-}"       # version to install (empty = latest release)
install_service=1

usage() {
	cat <<'EOF'
nexus installer

Options:
  --home <path>                  Install location (default: $HOME/.nexus).
  --ref <version>                nexus version to install (default: latest release),
                                 e.g. --ref v1.2.3.
  --no-service                   Skip systemd/launchctl service setup.
  -h, --help                     Show this help.

Environment:
  NEXUS_HOME   Same as --home.
  NEXUS_REF    Same as --ref.

After installing, add projects with: nexus project add <spec-path>
EOF
}

die() { echo "nexus install: $*" >&2; exit 1; }
info() { echo "==> $*"; }

# --- parse arguments ---
while [ $# -gt 0 ]; do
	case "$1" in
		--home) [ $# -ge 2 ] || die "--home needs a value"; NEXUS_HOME="$2"; shift 2 ;;
		--home=*) NEXUS_HOME="${1#--home=}"; shift ;;
		--ref) [ $# -ge 2 ] || die "--ref needs a value"; NEXUS_REF="$2"; shift 2 ;;
		--ref=*) NEXUS_REF="${1#--ref=}"; shift ;;
		--no-service) install_service=0; shift ;;
		--project|--project=*) die "--project is no longer supported; after install run: nexus project add <spec-path>" ;;
		-h|--help) usage; exit 0 ;;
		*) die "unknown argument: $1 (try --help)" ;;
	esac
done

# --- preflight ---
# git is needed at runtime (nexus clones and polls repos); curl downloads binaries.
command -v git >/dev/null 2>&1 || die "'git' is required"
command -v curl >/dev/null 2>&1 || die "'curl' is required"

# NEXUS_HOME may contain ~ or be relative; resolve to an absolute path.
mkdir -p "$NEXUS_HOME"
NEXUS_HOME=$(cd "$NEXUS_HOME" && pwd)
BIN="$NEXUS_HOME/bin"

info "installing nexus to $NEXUS_HOME"
mkdir -p "$BIN" "$NEXUS_HOME/repos" "$NEXUS_HOME/volumes" "$NEXUS_HOME/logs" "$NEXUS_HOME/env"

# --- download the prebuilt binaries ---

# detect_platform prints "<os>-<arch>" for the release asset names.
detect_platform() {
	_os=$(uname -s | tr '[:upper:]' '[:lower:]')
	_arch=$(uname -m)
	case "$_os" in linux|darwin) ;; *) return 1 ;; esac
	case "$_arch" in
		x86_64|amd64) _arch=amd64 ;;
		aarch64|arm64) _arch=arm64 ;;
		*) return 1 ;;
	esac
	printf '%s-%s' "$_os" "$_arch"
}

platform=$(detect_platform) || die "no prebuilt binaries for $(uname -s)/$(uname -m)"

if [ -n "$NEXUS_REF" ]; then
	base="$NEXUS_REPO_URL/releases/download/$NEXUS_REF"
	info "downloading prebuilt nexus $NEXUS_REF ($platform)"
else
	base="$NEXUS_REPO_URL/releases/latest/download"
	info "downloading prebuilt nexus (latest release, $platform)"
fi

for b in nexus nexus-pm; do
	curl -fsSL "$base/$b-$platform" -o "$BIN/.$b.new" \
		|| { rm -f "$BIN/.$b.new"; die "could not download $b ($platform) from $base — is there a published release?"; }
	chmod +x "$BIN/.$b.new"
	mv -f "$BIN/.$b.new" "$BIN/$b"
done

[ -x "$BIN/nexus" ] || die "install did not produce $BIN/nexus"
[ -x "$BIN/nexus-pm" ] || die "install did not produce $BIN/nexus-pm"
info "installed $BIN/nexus and $BIN/nexus-pm"

# --- service setup ---
setup_systemd() {
	command -v systemctl >/dev/null 2>&1 || return 1
	# A user bus must be reachable for `systemctl --user` to work.
	systemctl --user show-environment >/dev/null 2>&1 || return 1

	unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
	mkdir -p "$unit_dir"
	cat > "$unit_dir/nexus.service" <<EOF
[Unit]
Description=Nexus process manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=NEXUS_HOME=$NEXUS_HOME
ExecStart=$BIN/nexus-pm
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
EOF

	systemctl --user daemon-reload
	systemctl --user enable --now nexus.service
	info "systemd user service 'nexus' installed and started"
	info "  status: systemctl --user status nexus"
	return 0
}

setup_launchd() {
	command -v launchctl >/dev/null 2>&1 || return 1

	agent_dir="$HOME/Library/LaunchAgents"
	mkdir -p "$agent_dir"
	plist="$agent_dir/com.rdkal.nexus.plist"
	cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.rdkal.nexus</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN/nexus-pm</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>NEXUS_HOME</key>
		<string>$NEXUS_HOME</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>$NEXUS_HOME/logs/nexus-pm.log</string>
	<key>StandardOutPath</key>
	<string>$NEXUS_HOME/logs/nexus-pm.log</string>
</dict>
</plist>
EOF

	launchctl unload "$plist" >/dev/null 2>&1 || true
	launchctl load "$plist"
	info "launchd agent 'com.rdkal.nexus' installed and started"
	return 0
}

manual_instructions() {
	cat <<EOF

Could not set up an automatic service on this system. To run nexus manually:

    NEXUS_HOME=$NEXUS_HOME $BIN/nexus-pm

To run it under your own init system, point it at: $BIN/nexus-pm
with NEXUS_HOME=$NEXUS_HOME in the environment.
EOF
}

if [ "$install_service" -eq 1 ]; then
	case "$(uname -s)" in
		Linux)  setup_systemd || manual_instructions ;;
		Darwin) setup_launchd || manual_instructions ;;
		*)      manual_instructions ;;
	esac
else
	info "skipping service setup (--no-service)"
	manual_instructions
fi

# --- PATH setup ---
# Put $BIN on PATH so `nexus` works without a full path. Idempotently append to
# the shell rc files that exist, plus ~/.profile for login shells.
on_path=0
case ":${PATH}:" in *":$BIN:"*) on_path=1 ;; esac
if [ "$on_path" -eq 0 ]; then
	line="export PATH=\"$BIN:\$PATH\"   # added by nexus installer"
	updated=""
	for rc in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.zshrc"; do
		[ -e "$rc" ] || { [ "$rc" = "$HOME/.profile" ] || continue; : > "$rc"; }
		grep -qF "$BIN" "$rc" 2>/dev/null && continue
		printf '\n%s\n' "$line" >> "$rc"
		updated="$updated $rc"
	done
	[ -n "$updated" ] && info "added $BIN to PATH in:$updated"
fi

info "done"
cat <<EOF

Add a project to start deploying (open a new terminal, or run the line above first):

    nexus project add <spec-path>

for example the web dashboard (serves on port 7777):

    nexus project add github.com/rdkal/nexus/web
EOF
if [ "$on_path" -eq 0 ]; then
	printf '\nTo use nexus in this shell now, run:\n\n    export PATH="%s:$PATH"\n' "$BIN"
fi
