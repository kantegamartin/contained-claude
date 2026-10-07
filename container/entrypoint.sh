#!/bin/bash
set -e

# Source SDKMAN so java/maven are on PATH
if [ -f "$HOME/.sdkman/bin/sdkman-init.sh" ]; then
  source "$HOME/.sdkman/bin/sdkman-init.sh"
fi

# Copy host git config (mounted read-only at .gitconfig.host)
if [ -f "$HOME/.gitconfig.host" ]; then
  cp "$HOME/.gitconfig.host" "$HOME/.gitconfig"
fi

# Configure git to use GitHub CLI for authentication
if [ -n "$GH_TOKEN" ] && command -v gh >/dev/null 2>&1; then
  gh auth setup-git 2>/dev/null || true
fi

UPDATE_TIMEOUT_SECONDS=120
if [ -z "$CLAUDE_SKIP_UPDATE" ]; then
  timeout "$UPDATE_TIMEOUT_SECONDS" claude update \
    || echo "Claude Code update failed or timed out, starting the installed version"
fi

echo "Running as: $(id)"
ls -la ~/.claude/.credentials.json 2>/dev/null || echo "No credentials file found"

exec claude "$@"
