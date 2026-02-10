package apimetadata

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Resolver looks up endpoint metadata
type Resolver interface {
	// Resolve finds metadata for an HTTP request by extracting chi route context.
	Resolve(req *http.Request) *EndpointMetadata
}

// patternEntry holds a template pattern and its metadata pointer
type patternEntry struct {
	pattern  string
	metadata *EndpointMetadata
}

// StaticResolver implements Resolver using pre-indexed metadata
type StaticResolver struct {
	prefixes         []string
	exact            map[string]*EndpointMetadata // "METHOD:/path" -> metadata
	patternsByMethod map[string][]patternEntry    // method -> list of patterns
}

// NewStaticResolver creates a resolver from generated data.
// Pre-indexes patterns by method for O(1) exact lookup + O(patterns_for_method) template matching.
func NewStaticResolver(prefixes []string, metadataMap map[string]*EndpointMetadata) *StaticResolver {
	r := &StaticResolver{
		prefixes:         prefixes,
		exact:            make(map[string]*EndpointMetadata),
		patternsByMethod: make(map[string][]patternEntry),
	}

	for key, meta := range metadataMap {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		method, pattern := parts[0], parts[1]

		if isTemplatedPath(pattern) {
			r.patternsByMethod[method] = append(r.patternsByMethod[method], patternEntry{
				pattern:  pattern,
				metadata: meta,
			})
		} else {
			r.exact[key] = meta
		}
	}

	return r
}

// Resolve finds metadata for an HTTP request by extracting the chi route context.
func (r *StaticResolver) Resolve(req *http.Request) *EndpointMetadata {
	var routePath string
	if rctx := chi.RouteContext(req.Context()); rctx != nil {
		routePath = rctx.RoutePath
	}
	return r.Match(req.Method, req.URL.Path, routePath)
}

// Match finds metadata for a request given method, path, and optional route path from chi.
// routePath is the path from chi's RouteContext (e.g., "/devices/foo"), empty if unavailable.
func (r *StaticResolver) Match(method, path, routePath string) *EndpointMetadata {
	// Strategy 1: Use route path from chi if provided (stripped prefix)
	if routePath != "" {
		routeCandidates := r.normalizeCandidates(routePath)
		for _, candidate := range routeCandidates {
			key := method + ":" + candidate
			if meta, ok := r.exact[key]; ok {
				return meta
			}
		}
		// Check pattern-based entries against route path
		for _, entry := range r.patternsByMethod[method] {
			for _, candidate := range routeCandidates {
				if matchTemplatePath(entry.pattern, candidate) {
					return entry.metadata
				}
			}
		}
	}

	// Strategy 2: Exact path match (O(1))
	pathCandidates := r.normalizeCandidates(path)
	for _, candidate := range pathCandidates {
		key := method + ":" + candidate
		if meta, ok := r.exact[key]; ok {
			return meta
		}
	}

	// Strategy 3: Template pattern matching (O(patterns_for_method))
	for _, entry := range r.patternsByMethod[method] {
		for _, candidate := range pathCandidates {
			if matchTemplatePath(entry.pattern, candidate) {
				return entry.metadata
			}
		}
	}

	return nil
}

// normalizeCandidates generates lookup candidates by stripping known server prefixes.
// For example, "/api/v1/devices" with prefix "/api/v1" yields ["/api/v1/devices", "/devices"].
func (r *StaticResolver) normalizeCandidates(path string) []string {
	base := normalizePath(path)
	candidates := make([]string, 0, 1+len(r.prefixes))
	seen := map[string]struct{}{}
	addCandidate := func(p string) {
		if p == "" {
			return
		}
		if _, exists := seen[p]; exists {
			return
		}
		seen[p] = struct{}{}
		candidates = append(candidates, p)
	}

	addCandidate(base)
	for _, prefix := range r.prefixes {
		if prefix == "/" {
			continue
		}
		if base == prefix {
			addCandidate("/")
			continue
		}
		if strings.HasPrefix(base, prefix+"/") {
			stripped := strings.TrimPrefix(base, prefix)
			addCandidate(normalizePath(stripped))
		}
	}
	return candidates
}

// isTemplatedPath checks if a path contains template parameters like "{name}".
func isTemplatedPath(path string) bool {
	return strings.Contains(path, "{") && strings.Contains(path, "}")
}

// normalizePath ensures a path starts with "/" and has no trailing slash.
func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// matchTemplatePath checks if a path matches a pattern with "{param}" placeholders.
// For example, "/devices/{name}" matches "/devices/foo".
func matchTemplatePath(pattern, path string) bool {
	pattern = normalizePath(pattern)
	path = normalizePath(path)

	pSegs := splitPath(pattern)
	pathSegs := splitPath(path)
	if len(pSegs) != len(pathSegs) {
		return false
	}
	for i := range pSegs {
		ps := pSegs[i]
		if len(ps) >= 2 && ps[0] == '{' && ps[len(ps)-1] == '}' {
			continue
		}
		if ps != pathSegs[i] {
			return false
		}
	}
	return true
}

// splitPath splits a path into non-empty segments, e.g., "/api/v1/devices" -> ["api", "v1", "devices"].
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
