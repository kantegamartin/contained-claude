# Contained-Claude

Run Claude Code in a sandboxed environment for Java backend and JavaScript frontend development.

For a more thorough discussion, see [Containerizing Claude](https://medium.com/kantega/containerizing-claude-ef722831fb10) on Medium.

## Quick start

Requires [GitHub CLI](https://cli.github.com) (`gh auth login`) and either Podman or Docker. Use `--no-token` to skip the GitHub CLI requirement (push/PR must be done outside the sandbox).

```bash
./claude-container.sh
```

This builds a container image (first run only) and starts Claude with access to your project code, Git/GitHub, SSH agent, Java, Maven, and Node.js.

Two native sandbox variants are also available:

```bash
./claude-linux.sh     # Linux (Bubblewrap)
./claude-macos.sh     # macOS (sandbox-exec)
```

## Passing arguments to Claude

```bash
./claude-container.sh -p "run the tests"
```

## Options

### Container variant

```bash
./claude-container.sh --rebuild           # Force rebuild the image
./claude-container.sh --no-podman         # Disable container-in-container support
./claude-container.sh --no-token          # Run without GitHub token (push/PR outside sandbox)
./claude-container.sh --install-go-python # Include Go and Python in the image
./claude-container.sh --debug             # Start with a shell instead of Claude
JAVA_VERSION=21.0.5-tem ./claude-container.sh --rebuild   # Custom Java version
CONTAINER_CMD=docker ./claude-container.sh                # Use Docker instead of Podman
```

### Cleanup

```bash
./stopAllContainers.sh    # Stop all running containers
./cleanAllContainers.sh   # Full cleanup: containers, images, volumes, networks
```

## Contributing

See [CONTRIBUTE.md](CONTRIBUTE.md) for detailed documentation on architecture, mount tables, tool toggles, and platform comparison.
