package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"regexp"
	"strings"
	"sync"
)

// validVolumeName matches Docker/Podman named volume format: starts with
// alphanumeric, then alphanumeric/underscore/dot/dash. Anything else
// (paths, traversals, tilde, etc.) is rejected.
var validVolumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// route defines a single permitted API endpoint.
type route struct {
	methods   string         // comma-separated HTTP methods, or "*" for any
	pattern   *regexp.Regexp // must match the full path (after optional version prefix)
	childOnly bool           // if true, captured group 1 must be a child container ID
}

// p builds a regexp that accepts an optional /v1.XX version prefix.
func p(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`^(/v[\d.]+)?` + pattern + `$`)
}

// allowedRoutes is the exhaustive list of API endpoints the proxy will forward.
// Everything not listed here is blocked with HTTP 403.
var allowedRoutes = []route{
	// System / health
	{"GET", p(`/_ping`), false},
	{"GET", p(`/version`), false},
	{"GET", p(`/info`), false},
	{"GET", p(`/events`), false},
	{"GET,HEAD", p(`/system/df`), false},

	// Images — pull, inspect, list, tag, remove
	{"POST", p(`/images/create`), false},
	{"GET", p(`/images/json`), false},
	{"GET", p(`/images/(.+)/json`), false},
	{"POST", p(`/images/(.+)/tag`), false},
	{"DELETE", p(`/images/(.+)`), false},
	{"GET", p(`/images/search`), false},
	{"POST", p(`/images/prune`), false},

	// Containers — create (filtered), list, inspect
	{"POST", p(`/containers/create`), false},
	{"GET", p(`/containers/json`), false},
	{"GET", p(`/containers/([^/]+)/json`), false},
	{"GET", p(`/containers/([^/]+)/top`), true},
	{"GET", p(`/containers/([^/]+)/changes`), true},

	// Container lifecycle — child only
	{"POST", p(`/containers/([^/]+)/start`), true},
	{"POST", p(`/containers/([^/]+)/stop`), true},
	{"POST", p(`/containers/([^/]+)/kill`), true},
	{"POST", p(`/containers/([^/]+)/restart`), true},
	{"POST", p(`/containers/([^/]+)/pause`), true},
	{"POST", p(`/containers/([^/]+)/unpause`), true},
	{"POST", p(`/containers/([^/]+)/wait`), true},
	{"POST", p(`/containers/([^/]+)/resize`), true},
	{"POST", p(`/containers/([^/]+)/rename`), true},
	{"DELETE", p(`/containers/([^/]+)`), true},

	// Container I/O — child only
	{"POST", p(`/containers/([^/]+)/exec`), true},
	{"GET,PUT", p(`/containers/([^/]+)/archive`), true},
	{"GET", p(`/containers/([^/]+)/logs`), true},
	{"POST", p(`/containers/([^/]+)/attach`), true},

	// Exec — start/resize/inspect (exec IDs, not container IDs — no child check)
	{"POST", p(`/exec/([^/]+)/start`), false},
	{"POST", p(`/exec/([^/]+)/resize`), false},
	{"GET", p(`/exec/([^/]+)/json`), false},

	// Networks — create (filtered), inspect, list, connect, disconnect, remove
	{"POST", p(`/networks/create`), false},
	{"GET", p(`/networks`), false},
	{"GET", p(`/networks/([^/]+)`), false},
	{"POST", p(`/networks/([^/]+)/connect`), false},
	{"POST", p(`/networks/([^/]+)/disconnect`), false},
	{"DELETE", p(`/networks/([^/]+)`), false},
	{"POST", p(`/networks/prune`), false},

	// Volumes — create, inspect, list, remove
	{"POST", p(`/volumes/create`), false},
	{"GET", p(`/volumes`), false},
	{"GET", p(`/volumes/([^/]+)`), false},
	{"DELETE", p(`/volumes/([^/]+)`), false},
	{"POST", p(`/volumes/prune`), false},

	// Container prune (removes only stopped containers)
	{"POST", p(`/containers/prune`), false},
}

// dangerousNetworkDrivers are network drivers that bypass network isolation.
var dangerousNetworkDrivers = map[string]bool{
	"host":    true,
	"macvlan": true,
	"ipvlan":  true,
}

// createdContainers tracks container IDs created through this proxy,
// so we can allow operations on child containers but not on the sandbox itself.
var createdContainers = struct {
	sync.RWMutex
	ids map[string]bool
}{ids: make(map[string]bool)}

func main() {
	upstreamSocket := os.Getenv("UPSTREAM_SOCKET")
	if upstreamSocket == "" {
		upstreamSocket = findSocket()
	}
	if upstreamSocket == "" {
		log.Fatal("No container socket found. Set UPSTREAM_SOCKET.")
	}

	listenSocket := os.Getenv("LISTEN_SOCKET")
	if listenSocket == "" {
		listenSocket = "/tmp/filtered-podman.sock"
	}

	// Clean up stale socket file
	os.Remove(listenSocket)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "localhost"
		},
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", upstreamSocket)
			},
		},
	}

	handler := newHandler(proxy)

	listener, err := net.Listen("unix", listenSocket)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenSocket, err)
	}
	// Make socket accessible inside the container
	os.Chmod(listenSocket, 0666)

	log.Printf("Socket proxy: %s -> %s (allowlist mode, %d routes)", listenSocket, upstreamSocket, len(allowedRoutes))
	log.Fatal(http.Serve(listener, handler))
}

// newHandler creates the request handler. It accepts a backend (the reverse proxy
// or a test stub) so the same routing logic can be tested without a real socket.
func newHandler(backend http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		for _, rt := range allowedRoutes {
			matches := rt.pattern.FindStringSubmatch(path)
			if matches == nil {
				continue
			}
			if !methodAllowed(rt.methods, r.Method) {
				continue
			}

			// Child-only check: captured group after the version prefix
			if rt.childOnly && len(matches) >= 3 {
				id := matches[2]
				if !isChildContainer(id) {
					blocked(w, fmt.Sprintf("%s %s (not a child container)", r.Method, path))
					return
				}
			}

			// Special handling for container create — filter HostConfig
			if strings.HasSuffix(path, "/containers/create") && r.Method == http.MethodPost {
				handleContainerCreate(w, r, backend)
				return
			}

			// Special handling for network create — filter driver
			if strings.HasSuffix(path, "/networks/create") && r.Method == http.MethodPost {
				handleNetworkCreate(w, r, backend)
				return
			}

			backend.ServeHTTP(w, r)
			return
		}

		// No route matched — block
		blocked(w, fmt.Sprintf("%s %s", r.Method, path))
	})
}

func methodAllowed(allowed, method string) bool {
	if allowed == "*" {
		return true
	}
	for _, m := range strings.Split(allowed, ",") {
		if m == method {
			return true
		}
	}
	return false
}

// responseRecorder captures the response so we can inspect it (e.g. to extract container IDs).
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// handleContainerCreate filters the request body, then intercepts the response
// to track the created container ID.
func handleContainerCreate(w http.ResponseWriter, r *http.Request, backend http.Handler) {
	if err := filterCreate(w, r); err != nil {
		return // response already written
	}
	// Intercept response to track the created container ID
	recorder := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
	backend.ServeHTTP(recorder, r)
	if recorder.statusCode == http.StatusCreated || recorder.statusCode == http.StatusOK {
		var resp struct {
			Id string `json:"Id"`
		}
		if json.Unmarshal(recorder.body.Bytes(), &resp) == nil && resp.Id != "" {
			createdContainers.Lock()
			createdContainers.ids[resp.Id] = true
			if len(resp.Id) >= 12 {
				createdContainers.ids[resp.Id[:12]] = true
			}
			// Also track by name if one was provided in the query
			if name := r.URL.Query().Get("name"); name != "" {
				createdContainers.ids[name] = true
			}
			createdContainers.Unlock()
			log.Printf("Tracking created container: %.12s", resp.Id)
		}
	}
}

// handleNetworkCreate reads the request body, rejects dangerous network drivers,
// and forwards the request if safe.
func handleNetworkCreate(w http.ResponseWriter, r *http.Request, backend http.Handler) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var config map[string]interface{}
	if err := json.Unmarshal(body, &config); err != nil {
		http.Error(w, "invalid JSON in request body", http.StatusBadRequest)
		return
	}

	if driver, ok := config["Driver"].(string); ok {
		if dangerousNetworkDrivers[strings.ToLower(driver)] {
			http.Error(w, fmt.Sprintf("blocked: network driver %q not allowed", driver), http.StatusForbidden)
			return
		}
	}

	// Re-attach body for upstream
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	backend.ServeHTTP(w, r)
}

// filterCreate reads the container create request body, rejects unsafe
// configurations, and re-attaches the body for forwarding.
func filterCreate(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return err
	}

	var config map[string]interface{}
	if err := json.Unmarshal(body, &config); err != nil {
		http.Error(w, "invalid JSON in request body", http.StatusBadRequest)
		return err
	}

	if err := checkHostConfig(config); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return err
	}

	// Re-attach body for upstream
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return nil
}

func checkHostConfig(config map[string]interface{}) error {
	hc, ok := config["HostConfig"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Block privileged mode
	if priv, ok := hc["Privileged"].(bool); ok && priv {
		return fmt.Errorf("blocked: privileged mode")
	}

	// Block bind mounts unless the source is a named volume.
	// Named volumes match [a-zA-Z0-9][a-zA-Z0-9_.-]* — anything else
	// (absolute paths, relative paths, traversals, tilde) is rejected.
	if binds, ok := hc["Binds"].([]interface{}); ok {
		for _, b := range binds {
			bind, ok := b.(string)
			if !ok {
				continue
			}
			src := strings.Split(bind, ":")[0]
			if !validVolumeName.MatchString(src) {
				return fmt.Errorf("blocked: bind source %q is not a named volume", src)
			}
		}
	}

	// Block all bind mounts via Mounts array. Unlike Binds, the Source
	// field here is always a host path, so bind type is never safe.
	if mounts, ok := hc["Mounts"].([]interface{}); ok {
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			mountType, _ := mount["Type"].(string)
			if mountType == "bind" {
				source, _ := mount["Source"].(string)
				return fmt.Errorf("blocked: host bind mount %q", source)
			}
		}
	}

	// Block namespace sharing. "host" shares with the host, "container:<id>"
	// shares with another container. For PidMode and IpcMode, only allow
	// sharing with child containers (created through this proxy).
	if err := checkNamespaceMode(hc, "PidMode"); err != nil {
		return err
	}
	if err := checkNamespaceMode(hc, "IpcMode"); err != nil {
		return err
	}
	if err := checkNamespaceMode(hc, "UTSMode"); err != nil {
		return err
	}
	if net, ok := hc["NetworkMode"].(string); ok && net == "host" {
		return fmt.Errorf("blocked: host network namespace")
	}
	if userns, ok := hc["UsernsMode"].(string); ok && userns == "host" {
		return fmt.Errorf("blocked: host user namespace")
	}

	// Block Sysctls — kernel parameter modification
	if sysctls, ok := hc["Sysctls"].(map[string]interface{}); ok && len(sysctls) > 0 {
		return fmt.Errorf("blocked: sysctls not allowed")
	}

	// Block Tmpfs mounts
	if tmpfs, ok := hc["Tmpfs"].(map[string]interface{}); ok {
		for path := range tmpfs {
			return fmt.Errorf("blocked: tmpfs mount %q not allowed", path)
		}
	}

	// Only allow safe capabilities (allowlist approach)
	if capAdd, ok := hc["CapAdd"].([]interface{}); ok {
		for _, c := range capAdd {
			cap, _ := c.(string)
			if !safeCapabilities[strings.ToUpper(cap)] {
				return fmt.Errorf("blocked: capability %q not in allowlist", cap)
			}
		}
	}

	// Block device mappings
	if devices, ok := hc["Devices"].([]interface{}); ok && len(devices) > 0 {
		return fmt.Errorf("blocked: device mappings not allowed")
	}

	// Block security opt-outs
	if secOpts, ok := hc["SecurityOpt"].([]interface{}); ok {
		for _, s := range secOpts {
			opt, _ := s.(string)
			lower := strings.ToLower(opt)
			if strings.Contains(lower, "unconfined") || strings.Contains(lower, "disabled") {
				return fmt.Errorf("blocked: unsafe security option %q", opt)
			}
		}
	}

	return nil
}

// safeCapabilities is the allowlist of capabilities that child containers may add.
var safeCapabilities = map[string]bool{
	"NET_BIND_SERVICE": true,
	"CHOWN":            true,
	"DAC_OVERRIDE":     true,
	"FOWNER":           true,
	"FSETID":           true,
	"KILL":             true,
	"SETGID":           true,
	"SETUID":           true,
	"SETPCAP":          true,
	"SYS_CHROOT":       true,
	"MKNOD":            true,
	"AUDIT_WRITE":      true,
	"SETFCAP":          true,
}

func isChildContainer(id string) bool {
	createdContainers.RLock()
	defer createdContainers.RUnlock()
	return createdContainers.ids[id]
}

// checkNamespaceMode validates PidMode, IpcMode, or UTSMode. Blocks "host" and
// "container:<id>" unless the target is a child container.
func checkNamespaceMode(hc map[string]interface{}, key string) error {
	val, ok := hc[key].(string)
	if !ok || val == "" {
		return nil
	}
	if val == "host" {
		return fmt.Errorf("blocked: host %s namespace", key)
	}
	if strings.HasPrefix(val, "container:") {
		target := strings.TrimPrefix(val, "container:")
		if !isChildContainer(target) {
			return fmt.Errorf("blocked: %s shares namespace with non-child container %q", key, target)
		}
	}
	return nil
}

func blocked(w http.ResponseWriter, detail string) {
	log.Printf("Blocked: %s", detail)
	http.Error(w, fmt.Sprintf("blocked by socket proxy: %s", detail), http.StatusForbidden)
}

func findSocket() string {
	home := os.Getenv("HOME")
	candidates := []string{
		// Linux podman (rootless)
		fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid()),
		// macOS podman machine
		home + "/.local/share/containers/podman/machine/podman.sock",
		home + "/.local/share/containers/podman/machine/qemu/podman.sock",
		home + "/.local/share/containers/podman/machine/podman-machine-default/podman.sock",
		// Docker (Linux and macOS)
		"/var/run/docker.sock",
		home + "/.docker/run/docker.sock",
	}
	for _, s := range candidates {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return ""
}
