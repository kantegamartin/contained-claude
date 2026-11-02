# Web Guardrail Options

Exploration of options for adding an LLM-based guardrail to inspect web content fetched by Claude, primarily to detect malicious content and prompt injection.

## Context

### Where web fetches originate

| Source | How | Goes through your machine? |
|---|---|---|
| Claude's `WebFetch` / `WebSearch` tools | Executed server-side by Anthropic | No — result arrives in API response |
| `curl`, `wget` | Shell, in container | Yes |
| Python `urllib` | Shell, in container | Yes |
| Node `fetch` | Shell, in container | Yes |
| Java `HttpClient` | Shell, in container | Yes |

### Why the socket-proxy is not the right hook

The existing socket-proxy only intercepts **Docker/Podman API calls**. Regular HTTP/HTTPS traffic from any of the above tools bypasses it entirely.

### The Java problem

Java's HTTP client does **not** respect the standard `http_proxy`/`https_proxy` environment variables. It uses JVM system properties (`-Dhttp.proxyHost`, `-Dhttp.proxyPort`) instead. This makes env-var proxy approaches incomplete without explicitly configuring the JVM.

---

## Options

### Option 1: Claude Code post-tool-use hooks
Intercept `WebFetch` and `WebSearch` results in a hook before Claude reads them. Send the content to a guardrail LLM (e.g. Haiku) to detect prompt injection or malicious instructions.

- **Covers:** WebFetch, WebSearch
- **Does not cover:** curl, wget, Python, Node, Java shell calls
- **Cost:** Low — one cheap LLM call (Haiku, ~$0.001) per web fetch. Zero infra.
- **Complexity:** Low
- **Note:** Since WebFetch/WebSearch are server-side, the hook result is the **only** interception point available for those tools.

### Option 2: Env-var HTTP proxy (in container)
Set `http_proxy`/`https_proxy` environment variables in the container, plus explicit JVM args for Java. Route all compliant HTTP traffic through an inspecting proxy that calls a guardrail LLM.

- **Covers:** curl, wget, Python, Node (with env vars); Java (with explicit JVM args)
- **Does not cover:** WebFetch/WebSearch (server-side); runtimes that ignore the convention
- **Cost:** Medium — requires a proxy service running on the host, plus MITM CA cert for HTTPS inspection
- **Complexity:** Medium

### Option 3: Transparent network proxy (host iptables)
Host-side iptables/nftables rules redirect all outbound TCP port 80/443 from the container's network namespace to a transparent proxy, regardless of runtime.

- **Covers:** All HTTP/HTTPS from shell tools, regardless of language
- **Does not cover:** WebFetch/WebSearch (server-side)
- **Cost:** Medium-high — host-side network rule management, MITM CA for HTTPS
- **Complexity:** High — brittle with Podman network updates, requires `CAP_NET_ADMIN` or root on host

### Option 4: Network container (Podman-native full enforcement)
Run a dedicated proxy container that owns the network namespace. Run the Claude container with `--network container:<proxy-container>`. All traffic physically flows through the proxy container's network stack.

- **Covers:** All HTTP/HTTPS from shell tools
- **Does not cover:** WebFetch/WebSearch (server-side)
- **Cost:** Medium — proxy container to build and maintain, MITM CA for HTTPS
- **Complexity:** High — but fits the existing container-based architecture without requiring host root

---

## Summary table

| Option | Covers | Cost | Complexity |
|---|---|---|---|
| 1. Hooks + guardrail LLM | WebFetch, WebSearch | Low | Low |
| 2. Env-var proxy | curl, wget, Python, Node, Java (with JVM args) | Medium | Medium |
| 3. Transparent proxy (iptables) | All shell HTTP | Medium-high | High |
| 4. Network container | All shell HTTP | Medium | High |

---

## Recommendation

**Option 1 covers the highest-value prompt injection surface at the lowest cost.** WebFetch and WebSearch are the tools Claude uses when acting autonomously on web content — the primary attack vector. A hook-based guardrail intercepts exactly those results and requires no network infrastructure.

Options 2–4 add coverage for shell-based HTTP tools, but a compromised Claude invoking `curl` or Java's HTTP client to bypass a guardrail is a more sophisticated secondary attack. Combine with Option 1 if that threat model is relevant.
