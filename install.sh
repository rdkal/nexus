#!/usr/bin/env bash
# Usage:
#   From the web:  curl -fsSL https://raw.githubusercontent.com/rdkal/nexus/main/install.sh | bash -s -- <config-url>
#   Development:   ./install.sh <config-url>
set -euo pipefail

NEXUS_HOME="${NEXUS_HOME:-$HOME/.nexus}"
NEXUS_REPO="https://github.com/rdkal/nexus"

usage() {
    echo "Usage: $0 <config-url>"
    echo ""
    echo "  config-url  URL to a nexus.yaml file, or a git repo URL containing nexus.yaml"
    echo ""
    echo "Examples:"
    echo "  $0 https://raw.githubusercontent.com/org/repo/main/nexus.yaml"
    echo "  $0 https://github.com/org/config-repo"
    echo ""
    echo "Environment:"
    echo "  NEXUS_HOME  Installation directory (default: ~/.nexus)"
    exit 1
}

[[ $# -lt 1 ]] && usage
CONFIG_URL="$1"

# ── dependency check ──────────────────────────────────────────────────────────
echo "Checking dependencies..."
MISSING=()
for dep in git uv process-compose; do
    command -v "$dep" &>/dev/null || MISSING+=("$dep")
done
if [[ ${#MISSING[@]} -gt 0 ]]; then
    echo "Missing required tools: ${MISSING[*]}"
    echo ""
    echo "Install guides:"
    echo "  uv:              curl -LsSf https://astral.sh/uv/install.sh | sh"
    echo "  process-compose: https://github.com/F1bonacc1/process-compose/releases"
    exit 1
fi

# ── nexus source ──────────────────────────────────────────────────────────────
# Dev mode: if this script lives in the nexus repo, use it directly.
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
if [[ "$CONFIG_URL" == *.yaml || "$CONFIG_URL" == *.yml ]]; then
    # Direct URL to a YAML file
    curl -fsSL "$CONFIG_URL" -o "$CONFIG_FILE"
else
    # Git repo containing nexus.yaml
    CONFIG_REPO="$NEXUS_HOME/config"
    if [[ -d "$CONFIG_REPO/.git" ]]; then
        git -C "$CONFIG_REPO" pull --quiet
    else
        git clone --quiet "$CONFIG_URL" "$CONFIG_REPO"
    fi
    if [[ ! -f "$CONFIG_REPO/nexus.yaml" ]]; then
        echo "Error: nexus.yaml not found in config repo root"
        exit 1
    fi
    cp "$CONFIG_REPO/nexus.yaml" "$CONFIG_FILE"
fi

# ── clone apps ────────────────────────────────────────────────────────────────
echo "Setting up apps..."
(cd "$NEXUS_SRC" && NEXUS_HOME="$NEXUS_HOME" uv run python -m nexus.setup "$CONFIG_FILE")

# ── launch ────────────────────────────────────────────────────────────────────
echo ""
echo "Starting nexus..."
echo "  Nexus UI  → http://localhost:8080"
echo "  Prefect   → http://localhost:4200"
echo ""
cd "$NEXUS_SRC"
exec env NEXUS_HOME="$NEXUS_HOME" NEXUS_SRC="$NEXUS_SRC" uv run python -m nexus.start
