#!/usr/bin/env bash
# Usage:
#   From the web:  curl -fsSL https://raw.githubusercontent.com/rdkal/nexus/main/install.sh | bash -s -- <config-url>
#   Development:   ./install.sh [--home <dir>] <config-url>
set -euo pipefail

NEXUS_REPO="https://github.com/rdkal/nexus"

usage() {
    echo "Usage: $0 [--home <dir>] <config-url>"
    echo ""
    echo "  --home <dir>  Installation directory (default: ~/.nexus)"
    echo "  config-url    Local path to .yaml, URL to .yaml, or git repo containing nexus.yaml"
    exit 1
}

# ── argument parsing ──────────────────────────────────────────────────────────
NEXUS_HOME="${NEXUS_HOME:-$HOME/.nexus}"
CONFIG_URL=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --home|-H) [[ $# -lt 2 ]] && usage; NEXUS_HOME="$2"; shift 2 ;;
        --help|-h) usage ;;
        *) CONFIG_URL="$1"; shift ;;
    esac
done

[[ -z "$CONFIG_URL" ]] && usage

# ── dependency bootstrap ──────────────────────────────────────────────────────
ensure_uv() {
    command -v uv &>/dev/null && return
    echo "Installing uv..."
    curl -LsSf https://astral.sh/uv/install.sh | sh
    export PATH="$HOME/.local/bin:$PATH"
    command -v uv &>/dev/null || { echo "uv install failed"; exit 1; }
}

ensure_process_compose() {
    command -v process-compose &>/dev/null && return
    echo "Installing process-compose..."
    local os_name arch install_dir
    os_name="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$(uname -m)" in
        x86_64)         arch="amd64" ;;
        aarch64|arm64)  arch="arm64" ;;
        *) echo "Unsupported arch: $(uname -m)"; exit 1 ;;
    esac
    install_dir="${HOME}/.local/bin"
    mkdir -p "$install_dir"
    local url="https://github.com/F1bonacc1/process-compose/releases/latest/download/process-compose_${os_name}_${arch}.tar.gz"
    curl -fsSL "$url" | tar -xz -C "$install_dir" process-compose
    chmod +x "$install_dir/process-compose"
    export PATH="$install_dir:$PATH"
    command -v process-compose &>/dev/null || { echo "process-compose install failed"; exit 1; }
}

echo "Checking dependencies..."
ensure_uv
ensure_process_compose

# ── nexus source ──────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || pwd)"
if [[ -f "$SCRIPT_DIR/pyproject.toml" ]] && grep -q 'name = "nexus"' "$SCRIPT_DIR/pyproject.toml"; then
    NEXUS_SRC="$SCRIPT_DIR"
    echo "Development mode: using $NEXUS_SRC"
else
    NEXUS_SRC="$NEXUS_HOME/nexus"
    if [[ -d "$NEXUS_SRC/.git" ]]; then
        echo "Updating nexus..."
        git -C "$NEXUS_SRC" pull --quiet
    else
        echo "Cloning nexus..."
        git clone --quiet "$NEXUS_REPO" "$NEXUS_SRC"
    fi
fi

# ── Python deps ───────────────────────────────────────────────────────────────
echo "Installing Python dependencies..."
(cd "$NEXUS_SRC" && uv sync --quiet)

# ── config ────────────────────────────────────────────────────────────────────
mkdir -p "$NEXUS_HOME/apps"
CONFIG_FILE="$NEXUS_HOME/config.yaml"

echo "Fetching config from $CONFIG_URL..."
if [[ -f "$CONFIG_URL" ]]; then
    cp "$CONFIG_URL" "$CONFIG_FILE"
elif [[ "$CONFIG_URL" == *.yaml || "$CONFIG_URL" == *.yml ]]; then
    curl -fsSL "$CONFIG_URL" -o "$CONFIG_FILE"
else
    CONFIG_REPO="$NEXUS_HOME/config"
    if [[ -d "$CONFIG_REPO/.git" ]]; then
        git -C "$CONFIG_REPO" pull --quiet
    else
        git clone --quiet "$CONFIG_URL" "$CONFIG_REPO"
    fi
    [[ -f "$CONFIG_REPO/nexus.yaml" ]] || { echo "Error: nexus.yaml not found in config repo root"; exit 1; }
    cp "$CONFIG_REPO/nexus.yaml" "$CONFIG_FILE"
fi

# ── clone apps ────────────────────────────────────────────────────────────────
echo "Setting up apps..."
(cd "$NEXUS_SRC" && NEXUS_HOME="$NEXUS_HOME" uv run python -m nexus.setup "$CONFIG_FILE")

# ── startup on boot ───────────────────────────────────────────────────────────
UV_BIN="$(command -v uv)"

install_systemd_service() {
    local service_dir="$HOME/.config/systemd/user"
    mkdir -p "$service_dir"
    cat > "$service_dir/nexus.service" <<EOF
[Unit]
Description=Nexus orchestration service
After=network.target

[Service]
Type=simple
WorkingDirectory=${NEXUS_SRC}
Environment=NEXUS_HOME=${NEXUS_HOME}
Environment=NEXUS_SRC=${NEXUS_SRC}
ExecStart=${UV_BIN} run python -m nexus.start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
    echo "Installed systemd service: $service_dir/nexus.service"
}

install_launchd_plist() {
    local plist_dir="$HOME/Library/LaunchAgents"
    local plist="$plist_dir/com.nexus.agent.plist"
    mkdir -p "$plist_dir"
    cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.nexus.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>${UV_BIN}</string>
        <string>run</string>
        <string>python</string>
        <string>-m</string>
        <string>nexus.start</string>
    </array>
    <key>WorkingDirectory</key><string>${NEXUS_SRC}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>NEXUS_HOME</key><string>${NEXUS_HOME}</string>
        <key>NEXUS_SRC</key><string>${NEXUS_SRC}</string>
    </dict>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>${NEXUS_HOME}/nexus.log</string>
    <key>StandardErrorPath</key><string>${NEXUS_HOME}/nexus.log</string>
</dict>
</plist>
EOF
    echo "Installed launchd plist: $plist"
}

# ── launch ────────────────────────────────────────────────────────────────────
echo ""
echo "Starting nexus..."
echo "  Nexus UI  → http://localhost:8080"
echo "  Prefect   → http://localhost:4200"
echo ""

_OS="$(uname -s)"
if [[ "$_OS" == "Darwin" ]]; then
    install_launchd_plist
    _PLIST="$HOME/Library/LaunchAgents/com.nexus.agent.plist"
    launchctl unload "$_PLIST" 2>/dev/null || true
    launchctl load -w "$_PLIST"
    echo "Nexus will start automatically on login (launchd)."
elif [[ "$_OS" == "Linux" ]]; then
    install_systemd_service
    # Enable linger so the user service survives logout
    loginctl enable-linger "$(whoami)" 2>/dev/null || true
    if systemctl --user daemon-reload 2>/dev/null && \
       systemctl --user enable nexus 2>/dev/null && \
       systemctl --user start nexus 2>/dev/null; then
        echo "Nexus will start automatically on login (systemd --user)."
        echo "  Status: systemctl --user status nexus"
    else
        echo "Note: systemd user session not available — starting directly."
        cd "$NEXUS_SRC"
        exec env NEXUS_HOME="$NEXUS_HOME" NEXUS_SRC="$NEXUS_SRC" uv run python -m nexus.start
    fi
else
    cd "$NEXUS_SRC"
    exec env NEXUS_HOME="$NEXUS_HOME" NEXUS_SRC="$NEXUS_SRC" uv run python -m nexus.start
fi
