# Security Hardening Suggestions

Suggestions for letting Claude run with far fewer permission prompts while keeping the sandbox
secure.

## Where the real risk is

The container protects the host filesystem, but that is not where the valuable things are. The
credentials passed into the container are the exposure. Every command the agent runs inside the
container has access to all of the following:

| Exposure | Current state | What it enables |
|---|---|---|
| GitHub token | `gh auth token` is passed in as `GH_TOKEN`, with scopes `repo, workflow, gist` | Push or merge to any repo the user can reach, including shared four-eyes repos; rewrite CI workflows; publish data as a gist |
| SSH private key | `~/.ssh` is mounted, so `id_ed25519` is readable | The key can be copied out and stays valid after the session ends |
| Maven credentials | `~/.m2/settings.xml` is mounted and contains a `<password>` | Access to the package registry |
| Claude settings | `~/.claude` is mounted read-write and shared with the host | Plain Bash (`sed`, `python`) can rewrite permission settings, so the agent can grant itself permissions. The changes also apply to Claude sessions run directly on the host, outside the sandbox |
| Network egress | Unrestricted | Any of the above can be sent anywhere with `curl` |
| MCP tools (Jira, Confluence, Datadog, Slack) | Run through claude.ai, not inside the container | The container gives them no protection at all |

Once plain `Bash` is allowed, file-level rules such as denying `Read(~/.ssh/**)` do nothing:
`cat` gets around them. The balance is therefore to **loosen the shell and tighten the
credentials**. The container already contains the shell. The credentials are what needs limiting.

## Suggestions

In priority order: 1 has the biggest effect for the least effort, and 5 is the most work.

### 1. Replace the full GitHub token with a fine-grained PAT

Replace `gh auth token` in `claude-container.sh` with a fine-grained personal access token:

- Scope it to the repos you own.
- Grant `Contents: write` and `Pull requests: write` only.
- Don't grant administration, workflows or gists.

For shared repos, run with `--no-token` and push from the host.

This rules out the worst outcomes: a merge into a shared `main`, or a change to a CI workflow.

### 2. Bake the permission rules into the image as managed settings

Put the permission rules in `/etc/claude-code/managed-settings.json` in `Containerfile.claude`,
not in the mounted `~/.claude`.

- The container user is non-root and has no `sudo`, so the agent can't modify the file.
- Managed settings take priority over user and project settings.
- The file exists only inside the container, so host sessions keep their stricter defaults.

Proposed content:

```json
{
  "permissions": {
    "allow": [
      "Bash", "Edit", "Write", "Read",
      "mcp__claude_ai_Datadog__search_*", "mcp__claude_ai_Datadog__get_*",
      "mcp__claude_ai_Atlassian__get*", "mcp__claude_ai_Atlassian__search*"
    ],
    "ask": [
      "Bash(git push *)", "Bash(gh pr create *)", "Bash(gh pr merge *)",
      "Bash(gh api *)", "Bash(gh workflow *)", "Bash(gh release *)",
      "Bash(gh gist *)", "Bash(gh repo *)",
      "mcp__claude_ai_Atlassian__createJiraIssue",
      "mcp__claude_ai_Atlassian__editJiraIssue",
      "mcp__claude_ai_Atlassian__transitionJiraIssue",
      "mcp__claude_ai_Atlassian__addCommentToJiraIssue",
      "mcp__claude_ai_Atlassian__createConfluencePage",
      "mcp__claude_ai_Atlassian__updateConfluencePage"
    ]
  }
}
```

`gh api` must stay in the ask list. Without it, `gh api -X PUT repos/<owner>/<repo>/pulls/<n>/merge`
gets around the `gh pr merge` rule.

Everything local, such as builds, tests, file edits and `cd`, runs without a prompt. Anything that
publishes outside the container still asks.

### 3. Stop mounting the SSH private key

`claude-container.sh` already forwards the SSH agent socket when SSH remotes are detected, so
mounting `~/.ssh` as well adds nothing except exposure.

Mount only `~/.ssh/known_hosts` and `~/.ssh/config` (read-only), and rely on the forwarded agent.
The agent socket stops working when the session ends. A copied key keeps working.

### 4. Mount the Claude settings files read-only

Mount these files read-only on top of the read-write `~/.claude` mount:

```bash
-v "$HOME/.claude/settings.json:$CHOME/.claude/settings.json:ro"
```

Do the same for the project `.claude/settings.local.json` of the mounted project folder.

This closes the self-escalation path completely. The trade-off is that "Yes, and don't ask again"
can no longer save rules. With suggestion 2 in place, the permanent rules live in managed settings,
so that isn't needed.

### 5. Restrict network egress

This is the only suggestion that addresses the exfiltration row of the table. It is also the most
work:

- It needs a filtering proxy with an allowlist of hosts: Maven Central, the npm registry, GitHub
  and the Anthropic API.
- Java ignores `http_proxy` and `https_proxy`. The JVM has to be configured separately through
  `JAVA_TOOL_OPTIONS`.

`web-guardrail-options.md` covers the proxy options.

## Open question: auto mode and bypass mode

Claude Code's auto mode approves routine actions with a classifier instead of prompting. It could
be combined with the managed ask rules from suggestion 2.

It is unverified whether `ask` rules are still enforced in auto mode or in `bypassPermissions`
mode. Check that before relying on either mode. The combination known to work is broad `allow`
rules in the default mode, as in suggestion 2.
