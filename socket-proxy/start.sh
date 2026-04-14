#!/usr/bin/env bash
# Find the host container socket, build the proxy if needed, and start it.
#
# Requires: CONTAINER_CMD, SCRIPT_DIR, colors and os_type sourced
# Sets:     FILTERED_SOCK, SOCKET_PROXY_PID, SOCKET_PROXY_OK
# Defines:  cleanup_proxy()
#
# For claude-container.sh also sets: PODMAN_ARGS, PODMAN_ENV

PROXY_DIR="$SCRIPT_DIR/socket-proxy"
PROXY_BIN="$PROXY_DIR/socket-proxy"
FILTERED_SOCK="/tmp/claude-filtered-$$.sock"
SOCKET_PROXY_OK=false

# --- Find the host's container socket ---
HOST_SOCK=""
if [ "$CONTAINER_CMD" = "podman" ]; then
  if $darwin; then
    for sock in \
      "$HOME/.local/share/containers/podman/machine/podman.sock" \
      "$HOME/.local/share/containers/podman/machine/qemu/podman.sock" \
      "$HOME/.local/share/containers/podman/machine/podman-machine-default/podman.sock"; do
      [ -S "$sock" ] && HOST_SOCK="$sock" && break
    done
  else
    HOST_SOCK="/run/user/$(id -u)/podman/podman.sock"
    if [ ! -S "$HOST_SOCK" ]; then
      echo -e "${YELLOW}Starting host podman socket...${NC}"
      systemctl --user start podman.socket 2>/dev/null \
        || podman system service --time=0 "unix://$HOST_SOCK" &
      sleep 1
    fi
  fi
elif [ "$CONTAINER_CMD" = "docker" ]; then
  if [ -S "/var/run/docker.sock" ]; then
    HOST_SOCK="/var/run/docker.sock"
  elif [ -S "$HOME/.docker/run/docker.sock" ]; then
    HOST_SOCK="$HOME/.docker/run/docker.sock"
  else
    # Colima: check all profiles (~/.colima/<profile>/docker.sock)
    for sock in "$HOME"/.colima/*/docker.sock; do
      [ -S "$sock" ] && HOST_SOCK="$sock" && break
    done
  fi
fi
if [ ! -S "$HOST_SOCK" ]; then
  echo -e "${YELLOW}Warning: container socket not found. Container-in-container support disabled.${NC}" >&2
  return 0 2>/dev/null || true
fi

# --- Build the proxy binary ---
. "$PROXY_DIR/build.sh"

# --- Start the proxy ---
UPSTREAM_SOCKET="$HOST_SOCK" LISTEN_SOCKET="$FILTERED_SOCK" "$PROXY_BIN" &
SOCKET_PROXY_PID=$!
for i in $(seq 1 20); do
  [ -S "$FILTERED_SOCK" ] && break
  sleep 0.1
done
if [ ! -S "$FILTERED_SOCK" ]; then
  echo -e "${RED}Error: socket proxy failed to start${NC}" >&2
  kill "$SOCKET_PROXY_PID" 2>/dev/null
  return 0 2>/dev/null || true
fi

SOCKET_PROXY_OK=true
echo -e "${GREEN}Socket proxy active (PID $SOCKET_PROXY_PID)${NC}"

# --- Cleanup handler (caller should trap EXIT) ---
cleanup_proxy() {
  if [ -n "$SOCKET_PROXY_PID" ]; then
    kill "$SOCKET_PROXY_PID" 2>/dev/null
    rm -f "$FILTERED_SOCK"
  fi
}

# --- Container-mode flags (used by claude-container.sh) ---
PODMAN_ARGS="-v $FILTERED_SOCK:/tmp/podman.sock"
PODMAN_ENV="-e DOCKER_HOST=unix:///tmp/podman.sock
            -e TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/tmp/podman.sock
            -e TESTCONTAINERS_RYUK_DISABLED=true
            -e TESTCONTAINERS_HOST_OVERRIDE=host.containers.internal"
