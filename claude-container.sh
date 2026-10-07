#!/usr/bin/env bash

# Script to start Claude in a sandboxed container
# Works on both macOS and Linux
#
# Tested on
#   * (add your OS/version here)
#
# Usage:
#   ./claude-container.sh              # build (if needed) and run
#   ./claude-container.sh --rebuild    # force rebuild the image
#   ./claude-container.sh --no-podman  # disable container-in-container support
#   ./claude-container.sh --no-token   # run without GitHub token (push/PR outside sandbox)
#   ./claude-container.sh --install-go-python  # include Go and Python in image
#   ./claude-container.sh --no-update  # skip the Claude Code update check at start
#   ./claude-container.sh -p "prompt"  # pass arguments through to claude
#
# To customise the Java version:
#   JAVA_VERSION=21.0.5-tem ./claude-container.sh --rebuild

# Exploratory prompt to Claude:
# You're running in a container. I'll be using this setup to develop Java backend and Javascript frontend.
# You should have access to editing my code, running tests and committing code to GitHub.
# Please verify that you have what you need.

SCRIPT_DIR="$( cd "$( dirname "$0" )" && pwd )"
. "$SCRIPT_DIR/lib/os_type"
. "$SCRIPT_DIR/lib/colors"

IMAGE_NAME="claude-sandbox"
CONTAINER_NAME="claude-sandbox-$$"
. "$SCRIPT_DIR/lib/container_cmd"
ENTRY_DIR="$SCRIPT_DIR/container"
USERNAME=$(whoami)

# --- Parse script arguments ---

REBUILD=false
ENTRYPOINT=""
DEBUG=false
PODMAN_MODE="socket"
GH_TOKEN_ENABLED=true
INSTALL_GO_PYTHON=false
UPDATE_CLAUDE=true
PROJECT_FOLDER="$PWD"
CLAUDE_ARGS=()
for arg in "$@"; do
  case "$arg" in
    --rebuild) REBUILD=true ;;
    --debug) DEBUG=true ;;
    --no-podman) PODMAN_MODE="none" ;;
    --no-token) GH_TOKEN_ENABLED=false ;;
    --install-go-python) INSTALL_GO_PYTHON=true ;;
    --no-update) UPDATE_CLAUDE=false ;;
    --folder) PROJECT_NAME="$1"; shift ;;
    *) CLAUDE_ARGS+=("$arg") ;;
  esac
done
PROJECT_NAME=$(echo "${PROJECT_FOLDER##*/}")

# --- GitHub CLI authentication ---
if $GH_TOKEN_ENABLED; then
  if ! command -v gh &>/dev/null; then
    echo -e "${RED}Error: gh (GitHub CLI) not found. Install from https://cli.github.com${NC}" >&2
    exit 1
  fi
  gh auth token &>/dev/null || gh auth login
fi


if $DEBUG; then
  echo "$REBUILD"
  echo "$CLAUDE_ARGS"
fi

# --- Build image if needed ---

if $REBUILD || ! $CONTAINER_CMD image inspect "$IMAGE_NAME" >/dev/null 2>&1; then
  echo -e "${GREEN}Building Claude sandbox image...${NC}"
  $CONTAINER_CMD build \
    --build-arg USERNAME="$USERNAME" \
    ${JAVA_VERSION:+--build-arg JAVA_VERSION="$JAVA_VERSION"} \
    --build-arg INSTALL_GO_PYTHON="$INSTALL_GO_PYTHON" \
    -t "$IMAGE_NAME" \
    -f "$SCRIPT_DIR/Containerfile.claude" \
    "$ENTRY_DIR"
fi

# --- Prerequisites ---

mkdir -p "$HOME/.claude" "$HOME/.m2"
[ ! -f "$HOME/.claude.json" ] && echo "{}" > "$HOME/.claude.json"
[ ! -f "$HOME/.claude.json.backup" ] && cp "$HOME/.claude.json" "$HOME/.claude.json.backup"

# --- Check for SSH remotes ---

CHOME="/home/$USERNAME"
. "$SCRIPT_DIR/lib/detect_ssh_remotes.sh"
SSH_MOUNT=""
if [ -n "$SSH_REPOS" ]; then
  echo -e "${YELLOW}Mounting ~/.ssh into container.${NC}"
  SSH_MOUNT="-v $HOME/.ssh:$CHOME/.ssh:ro"
fi

# --- SSH agent forwarding (only when SSH remotes detected) ---

SSH_ARGS=""
if [ -n "$SSH_REPOS" ]; then
  if $darwin; then
    # Docker Desktop for Mac provides a magic socket for SSH agent forwarding
    SSH_ARGS="-v /run/host-services/ssh-auth.sock:/run/host-services/ssh-auth.sock
              -e SSH_AUTH_SOCK=/run/host-services/ssh-auth.sock"
  elif [ -n "$SSH_AUTH_SOCK" ]; then
    SSH_ARGS="-v $SSH_AUTH_SOCK:$SSH_AUTH_SOCK
              -e SSH_AUTH_SOCK=$SSH_AUTH_SOCK"
  fi
fi

# --- Run ---

if $DEBUG; then
  ENTRYPOINT=" --entrypoint /home/$USERNAME/.empty-entrypoint.sh"
fi

# --- Check available ports ---
CONTAINER_PORTS="3000 5005 8080"
PORT_ARGS=""
is_port_in_use() {
  if command -v ss &>/dev/null; then
    ss -tlnH "sport = :$1" 2>/dev/null | grep -q .
  else
    lsof -iTCP:"$1" -sTCP:LISTEN -P -n &>/dev/null
  fi
}
for port in $CONTAINER_PORTS; do
  if ! is_port_in_use "$port"; then
    PORT_ARGS="$PORT_ARGS -p $port:$port"
  else
    echo -e "${YELLOW}Warning: port $port is in use on the host, skipping mapping.${NC}"
  fi
done

# --- Container-in-container support ---
PODMAN_ARGS=""
PODMAN_ENV=""
SOCKET_PROXY_PID=""
if [ "$PODMAN_MODE" = "socket" ]; then
  . "$SCRIPT_DIR/socket-proxy/start.sh"
  if ! ${SOCKET_PROXY_OK:-false}; then
    echo -e "${RED}Error: socket proxy required but failed to start${NC}" >&2
    exit 1
  fi
  trap cleanup_proxy EXIT
fi

GH_TOKEN_ARG=""
if $GH_TOKEN_ENABLED; then
  GH_TOKEN_ARG="-e GH_TOKEN=$(gh auth token)"
fi

UPDATE_ARG=""
if ! $UPDATE_CLAUDE; then
  UPDATE_ARG="-e CLAUDE_SKIP_UPDATE=1"
fi

# Claude Code's native install lives in ~/.local. Without a volume, every update is lost with the
# --rm container. On first use the volume is seeded from the image.
CLAUDE_INSTALL_VOLUME="$IMAGE_NAME-local"

$CONTAINER_CMD run --rm -it \
  --name "$CONTAINER_NAME" \
  --hostname claude-sandbox \
  --userns=keep-id \
  $PODMAN_ARGS \
  \
  -v "$PROJECT_FOLDER:$CHOME/$PROJECT_NAME" \
  -w "$CHOME/$PROJECT_NAME" \
  \
  -v "$HOME/.claude:$CHOME/.claude" \
  -v "$HOME/.claude.json:$CHOME/.claude.json" \
  -v "$HOME/.claude.json.backup:$CHOME/.claude.json.backup" \
  -v "$CLAUDE_INSTALL_VOLUME:$CHOME/.local" \
  \
  -e CHROME_BIN=/usr/bin/google-chrome \
  \
  -v "$HOME/.m2:$CHOME/.m2" \
  \
  $SSH_MOUNT \
  -v "$HOME/.gitconfig:$CHOME/.gitconfig.host:ro" \
  \
  $SSH_ARGS \
  \
  $PORT_ARGS \
  \
  $GH_TOKEN_ARG \
  $UPDATE_ARG \
  $PODMAN_ENV \
  \
  $ENTRYPOINT \
  \
  "$IMAGE_NAME" "${CLAUDE_ARGS[@]}"
