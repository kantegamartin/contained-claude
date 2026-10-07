# Container-Claude

Scripts for running Claude Code in a sandboxed environment for developing with Java backend and Javascript frontend.

Three sandbox variants are provided. Pick the one that fits your platform:

| Script | Platform | Sandbox technology | Isolation level |
|---|---|---|---|
| `claude-linux.sh` | Linux | bwrap (Bubblewrap) | Full namespace isolation |
| `claude-macos.sh` | macOS | sandbox-exec (Seatbelt) | File-system access control |
| `claude-container.sh` | Linux + macOS | OCI container (Podman/Docker) | Full namespace isolation |

All variants give Claude access to: your project code, Git/GitHub, SSH agent, Java (SDKMAN + Maven), Node.js, and a container runtime (Podman/Docker).

## Prerequisites

- **GitHub CLI** (`gh`) -- authenticated (`gh auth login`); not required with `--no-token`
- **SSH agent** running with your keys loaded
- **SDKMAN** with Java and Maven installed (Linux/macOS native variants)
- **Podman** or **Docker** (for `claude-container.sh`, optional for others)
- **Claude Code** installed (`npm install -g @anthropic-ai/claude-code` or standalone)

## Quick start

```bash
# Linux
./claude-linux.sh

# macOS (native sandbox)
./claude-macos.sh

# macOS or Linux (container)
./claude-container.sh
```

Arguments are passed through to Claude:

```bash
./claude-linux.sh -p "run the tests"
```

To work on a project other than the current directory:

```bash
./claude-linux.sh --folder ~/projects/myapp
./claude-macos.sh --folder ~/projects/myapp
./claude-container.sh --folder ~/projects/myapp
```

## claude-linux.sh (Linux / bwrap)

Tested on Ubuntu 25.10. Requires `bwrap` (`apt install bubblewrap`).

Uses Bubblewrap to create an isolated sandbox with explicit bind mounts. Only paths that are explicitly mounted are visible inside the sandbox.

### What gets mounted

| Path | Mode | Purpose |
|---|---|---|
| `$PWD` | read-write | Project code |
| `~/.claude`, `~/.claude.json` | read-write | Claude config and state |
| `~/.sdkman` | read-only | Java/Maven via SDKMAN |
| `~/.m2` | read-write | Maven local repository |
| `/usr/share/nodejs`, `/usr/share/npm` | read-only | Node.js / npm |
| `~/.ssh/known_hosts`, `~/.ssh/config` | read-only | SSH config (only if SSH remotes detected) |
| `$SSH_AUTH_SOCK` | read-write | SSH agent socket (only if SSH remotes detected) |
| `~/.gitconfig` | read-only | Git configuration |
| Podman socket + shares | read-only/read-write | Container runtime |
| `~/4claude/usrlocal` -> `/usr/local` | read-write | Writable area for tool installs |

### Tool toggles

Each tool section can be disabled by changing the flag in the script, or overridden by pre-setting the environment variable:

```bash
# In the script:
if [ -z "$SETUP_JAVA" -a true ]; then    # change 'true' to 'false' to disable
```

```bash
# Or from the environment:
SETUP_JAVA="" ./claude-linux.sh                  # use built-in defaults
SETUP_JAVA="--ro-bind ..." ./claude-linux.sh     # provide custom bwrap args
```

Sections: `SETUP_JAVA` (on), `SETUP_JAVASCRIPT` (on), `SETUP_PODMAN` (on), `SETUP_PYTHON` (off).

## claude-macos.sh (macOS / sandbox-exec)

Requires macOS with `sandbox-exec` (ships with the OS). Detects Homebrew on both Apple Silicon (`/opt/homebrew`) and Intel (`/usr/local`).

Uses Apple's Seatbelt sandbox profile to restrict file-system access. Unlike bwrap, all paths technically exist but unauthorized access is denied by the kernel.

**Note:** `sandbox-exec` is deprecated by Apple but still works through macOS 15 Sequoia. For stronger isolation on macOS, use the container variant.

### What gets allowed

| Path | Mode | Purpose |
|---|---|---|
| `$PWD` | read-write | Project code |
| `~/.claude`, `~/.claude.json` | read-write | Claude config and state |
| `~/.sdkman` | read-only | Java/Maven via SDKMAN |
| `~/.m2` | read-write | Maven local repository |
| `~/.npm`, `~/.nvm` | read-write/read-only | Node.js / npm |
| `~/.ssh` | read-only | SSH keys and config (only if SSH remotes detected) |
| `$SSH_AUTH_SOCK` | read-write | SSH agent socket (only if SSH remotes detected) |
| `~/.gitconfig` | read-only | Git configuration |
| `$HOMEBREW_PREFIX` | read-only | Homebrew tools and libraries |
| `/usr/bin`, `/usr/lib`, etc. | read-only | System binaries and libraries |
| Podman/Docker socket | read-write | Container runtime |
| `/tmp`, `$TMPDIR` | read-write | Temp files |
| Everything else | **denied** | |

Same tool toggle pattern as the Linux variant (`SETUP_JAVA`, `SETUP_JAVASCRIPT`, `SETUP_PODMAN`, `SETUP_PYTHON`).

## claude-container.sh (OCI container, cross-platform)

Works on both macOS and Linux. Prefers Podman, falls back to Docker (or set `CONTAINER_CMD=docker`).

Builds a Ubuntu 25.10 container image with all tools pre-installed, then runs Claude inside it. Since tools live in the image (not mounted from the host), macOS host binaries are not a concern.

### Image contents

- Ubuntu 25.10
- Node.js 24 LTS
- GitHub CLI
- Claude Code (via npm)
- SDKMAN + Java (default LTS) + Maven
- Go + Python (optional, via `--install-go-python`)

### What gets mounted

| Host path | Container path | Mode | Purpose |
|---|---|---|---|
| `$PWD` | `/home/$USER/$PROJECT` | read-write | Project code |
| `~/.claude` | `/home/$USER/.claude` | read-write | Claude config |
| `~/.claude.json` | `/home/$USER/.claude.json` | read-write | Claude settings |
| `~/.m2` | `/home/$USER/.m2` | read-write | Maven cache (shared with host) |
| `~/.ssh` | `/home/$USER/.ssh` | read-only | SSH keys (only if SSH remotes detected) |
| `~/.gitconfig` | `/home/$USER/.gitconfig.host` | read-only | Copied at startup, then `gh auth setup-git` runs |

SSH agent forwarding is handled automatically (Docker Desktop magic socket on macOS, direct bind on Linux), but only when SSH remotes are detected in the project folder.

Container-in-container support (for Testcontainers etc.) works through a **filtering socket proxy** that sits between the sandbox and the host's container runtime. Containers started by Testcontainers run as siblings on the host, not nested inside the sandbox. The script auto-detects the socket path for both Podman (`/run/user/$UID/podman/podman.sock`) and Docker (`/var/run/docker.sock` or `~/.docker/run/docker.sock`). Use `--no-podman` to disable this entirely.

See the [Socket proxy](#socket-proxy) section below for details on how the proxy hardens this communication channel.

`~/.ssh` is only mounted if SSH remotes are detected in the project folder — repos using `git@` or `ssh://` URLs. A warning is printed listing the affected repos. Repos under `target/` and `node_modules/` are skipped during the scan.

### Usage

```bash
# First run (builds the image, ~1-2 GB)
./claude-container.sh

# Force rebuild (e.g. to change Java version)
./claude-container.sh --rebuild

# Start without the Claude Code update check (e.g. offline)
./claude-container.sh --no-update

# Custom Java version
JAVA_VERSION=21.0.5-tem ./claude-container.sh --rebuild

# Run without GitHub token (push/PR must be done outside the sandbox)
./claude-container.sh --no-token

# Include Go and Python in the image (useful for socket proxy development)
./claude-container.sh --install-go-python --rebuild

# Use Docker instead of Podman
CONTAINER_CMD=docker ./claude-container.sh
```

### Files

| File | Purpose |
|---|---|
| `claude-container.sh` | Build and run script |
| `Containerfile.claude` | Container image definition |
| `container/entrypoint.sh` | Startup script (sources SDKMAN, configures git auth) |
| `stopAllContainers.sh` | Stop and remove all running containers |
| `cleanAllContainers.sh` | Full container/image/volume/network cleanup |
| `socket-proxy/` | Filtering proxy for Podman/Docker socket (see below) |

## Socket proxy

Mounting a raw container socket into a sandbox is dangerous — it gives the sandboxed process full control over the host's container runtime. An agent could create a privileged container, bind-mount the host filesystem, escape namespaces, or add dangerous capabilities. This effectively makes the sandbox boundary meaningless.

The socket proxy closes this gap. It is a Go reverse proxy that forwards requests from the sandbox to the host's Podman (or Docker) socket, applying a strict **allowlist with default block**:

- **Every request is denied by default.** Only explicitly listed API endpoints and HTTP methods are forwarded. Anything not on the list gets a `403 Forbidden`.
- **Request bodies are inspected.** Even on allowed endpoints like container create, the proxy parses the JSON body and rejects dangerous configurations before the request reaches the host runtime.
- **Child container isolation.** The proxy tracks which containers were created through it. Lifecycle operations (start, stop, kill, exec, logs, delete) are only permitted on those child containers — the sandbox cannot manipulate the sandbox container itself or any other container on the host.

### What the proxy blocks

| Attack vector | How it's blocked |
|---|---|
| Privileged containers | Rejects `Privileged: true` in create body |
| Host filesystem access | Blocks all bind mounts (absolute paths, relative paths, path traversal); only named volumes are allowed |
| Namespace escapes | Blocks `PidMode`, `IpcMode`, `UTSMode`, `NetworkMode`, `UsernsMode` set to `"host"` |
| Dangerous capabilities | Allowlists safe caps only; blocks `SYS_ADMIN`, `SYS_PTRACE`, `NET_RAW`, `ALL` |
| Security opt-outs | Blocks `seccomp=unconfined`, `apparmor=disabled` |
| Host devices | Blocks `/dev/*` device mappings |
| Kernel tuning | Blocks sysctls and tmpfs mounts |
| Dangerous network drivers | Blocks `host`, `macvlan`, `ipvlan`; allows `bridge` and isolated drivers |
| Image build / commit | `/build` and `/commit` endpoints not in allowlist |
| Swarm / plugins / secrets | Not in allowlist |

### What the proxy allows

Testcontainers and similar frameworks need a working container API. The proxy permits:

- **Container lifecycle** (create, start, stop, kill, remove) — with body filtering on create, and restricted to child containers
- **Container I/O** (exec, logs, attach, archive) — child containers only
- **Images** (pull, list, inspect, tag, remove, prune)
- **Networks** (create, list, inspect, connect, disconnect, remove, prune) — safe drivers only
- **Volumes** (create, list, inspect, remove, prune) — named volumes only
- **System** (ping, version, info, events, disk usage)

### How it runs

The proxy is started automatically by `claude-container.sh` (via `socket-proxy/start.sh`) before the sandbox launches:

```text
Host Podman socket  ◀──  Socket Proxy  ◀──  /tmp/podman.sock (inside container)
                        (allowlist)
```

1. `start.sh` detects the host socket (Podman or Docker)
2. Builds the proxy binary if needed (using a Go container, so Go is not required on the host)
3. Starts the proxy in the background, listening on a unique socket (`/tmp/claude-filtered-$$.sock`)
4. The filtered socket is mounted into the container and exposed as `DOCKER_HOST`
5. Testcontainers environment variables are set automatically (`DOCKER_HOST`, `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE`, `TESTCONTAINERS_RYUK_DISABLED`)
6. The proxy process is cleaned up when the sandbox exits

### Files

| File | Purpose |
|---|---|
| `socket-proxy/main.go` | Proxy implementation (allowlist, body filtering, child tracking) |
| `socket-proxy/main_test.go` | Unit tests |
| `socket-proxy/start.sh` | Host-side launch script (build, start, configure) |
| `socket-proxy/build.sh` | Builds the Go binary via container (no host Go needed) |
| `socket-proxy/test-blocked.sh` | Integration tests — verifies attack vectors are blocked |
| `socket-proxy/test-vulns.sh` | Vulnerability regression tests |

### Development

```bash
cd socket-proxy

# Build and test locally (requires Go 1.22+)
go build -o socket-proxy .
go test -v ./...

# Or build via container (no Go required)
./build.sh

# Run integration tests against a live proxy
./test-blocked.sh
./test-vulns.sh
```

## Utility scripts

### open_url.sh

Strips whitespace from a URL and opens it in the default browser. Useful when a URL is split across multiple lines in terminal output:

```bash
./open_url.sh https://example.com/ \
  long/path
# strips whitespace → opens https://example.com/long/path in browser
```

### lib/ (sourced helpers)

These scripts are sourced by the launcher scripts and live in the `lib/` directory.

**os_type** — OS detection. Sets boolean flags:

```bash
. ./lib/os_type
if $darwin; then echo "macOS"; fi
if $linux; then echo "Linux"; fi
```

**container_cmd** — Container runtime detection. Sets `CONTAINER_CMD` (prefers Podman over Docker). Override by pre-setting the variable:

```bash
. ./lib/container_cmd
$CONTAINER_CMD run ...

# Use Docker explicitly
CONTAINER_CMD=docker ./claude-container.sh
```

**detect_ssh_remotes.sh** — SSH remote detection. Source it (with `PROJECT_FOLDER` set) to populate `SSH_REPOS`. Prints a warning listing repos with SSH remotes. Skips `target/` and `node_modules/`. Used by all three launcher scripts to conditionally mount `~/.ssh` and forward `SSH_AUTH_SOCK`.

```bash
PROJECT_FOLDER="$PWD"
. ./lib/detect_ssh_remotes.sh
if [ -n "$SSH_REPOS" ]; then
  echo "SSH remotes found"
fi
```

**colors** — ANSI color variables. Source it for use in scripts, or run directly to preview all colors:

```bash
. ./lib/colors
echo -e "${RED}error${NC}"
```

## Comparison

|  | bwrap (Linux) | sandbox-exec (macOS) | OCI container (cross-platform) |
|---|---|---|---|
| PID isolation | yes | no | yes |
| Mount isolation | yes | no | yes |
| Network isolation | opt-in (shared by default) | no | opt-in (shared by default) |
| File-system isolation | allowlist (bind mounts) | denylist (Seatbelt rules) | allowlist (volume mounts) |
| Tools from | host | host | image |
| Java version | host's SDKMAN | host's SDKMAN | built into image |
| Update Claude | automatic (host binary) | automatic (host binary) | at every start (kept in the `claude-sandbox-local` volume) |
| Startup time | instant | instant | instant (after first build) |
| Image size | N/A | N/A | ~1-2 GB |

## Suggested first prompt

After launching Claude in any sandbox, verify the setup:

> You're running in a sandbox. I'll be using this setup to develop Java backend and Javascript frontend. You should have access to editing my code, running tests and committing code to GitHub. Please verify that you have what you need.
