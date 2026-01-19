package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"sigs.k8s.io/yaml"
)

type OpenAPIServer struct {
	URL string `yaml:"url"`
}

type OpenAPISpec struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Servers []OpenAPIServer           `yaml:"servers"`
	Paths   map[string]map[string]any `yaml:"paths"`
}

type endpointKey struct {
	Method, URLPattern string
}

type parsedEndpoint struct {
	OperationID  string
	Resource     string
	Action       string
	Version      string
	DeprecatedAt *time.Time
}

type EndpointMetadataVersion struct {
	Version      string
	DeprecatedAt *time.Time
}

type EndpointOutput struct {
	URLPattern  string
	Method      string
	OperationID string
	Resource    string
	Action      string
	Versions    []EndpointMetadataVersion
}

type ConstEntry struct {
	Name  string
	Value string
}

type TemplateData struct {
	Package        string
	Endpoints      []EndpointOutput
	ServerPrefixes []string
	ResourceConsts []ConstEntry
	ActionConsts   []ConstEntry
}

const (
	keyOperationID   = "operationId"
	keyXRBAC         = "x-rbac"
	keyXResource     = "x-resource"
	keyXDeprecatedAt = "x-deprecated-at"
	keyResource      = "resource"
	keyAction        = "action"
)

var (
	httpMethods = []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}
	versionRe   = regexp.MustCompile(`^v(\d+)(alpha|beta)?(\d*)$`)
)

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// parseDeprecatedAt parses an ISO date string (YYYY-MM-DD) to *time.Time
func parseDeprecatedAt(dateStr string) *time.Time {
	dateStr = strings.TrimSpace(dateStr)
	if dateStr == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		log.Printf("Warning: invalid x-deprecated-at %q: %v", dateStr, err)
		return nil
	}
	return &t
}

// inferActionFromMethod maps HTTP methods to K8s RBAC verbs
func inferActionFromMethod(method, path string) string {
	// Check if this is a collection endpoint
	// A collection has no path parameters, like /api/v1/devices
	// A resource has path parameters, like /api/v1/devices/{name} or /api/v1/devices/{name}/status
	isCollection := !strings.Contains(path, "{")

	switch strings.ToUpper(method) {
	case "GET":
		if isCollection {
			return "list"
		}
		return "get"
	case "POST":
		return "create"
	case "PUT":
		return "update"
	case "PATCH":
		return "patch"
	case "DELETE":
		if isCollection {
			return "deletecollection"
		}
		return "delete"
	default:
		return ""
	}
}

// Returns (major, stabilityRank, stabilityNum) where lower stabilityRank is better:
// stable=0, beta=1, alpha=2, unknown=3.
func versionKey(v string) (int, int, int) {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return 0, 3, 0
	}
	major, _ := strconv.Atoi(m[1])

	stability := 3
	switch m[2] {
	case "":
		stability = 0
	case "beta":
		stability = 1
	case "alpha":
		stability = 2
	}

	num := 0
	if m[3] != "" {
		num, _ = strconv.Atoi(m[3])
	}
	return major, stability, num
}

func extractServerPrefixes(servers []OpenAPIServer) []string {
	if len(servers) == 0 {
		return nil
	}
	prefixes := make([]string, 0, len(servers))
	for _, server := range servers {
		if prefix, ok := normalizeServerPrefix(server.URL); ok {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func normalizeServerPrefix(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if parsed, err := url.Parse(raw); err == nil && (parsed.Scheme != "" || parsed.Host != "") {
		raw = parsed.Path
	}
	if raw == "" {
		raw = "/"
	}
	return normalizePath(raw), true
}

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

func dedupeAndSortPrefixes(prefixSet map[string]struct{}) []string {
	if len(prefixSet) == 0 {
		return nil
	}
	prefixes := make([]string, 0, len(prefixSet))
	for prefix := range prefixSet {
		prefixes = append(prefixes, normalizePath(prefix))
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i]) != len(prefixes[j]) {
			return len(prefixes[i]) > len(prefixes[j])
		}
		return prefixes[i] < prefixes[j]
	})
	return prefixes
}

func buildConstEntries(prefix string, values map[string]struct{}) []ConstEntry {
	if len(values) == 0 {
		return nil
	}
	sorted := make([]string, 0, len(values))
	for value := range values {
		sorted = append(sorted, value)
	}
	sort.Strings(sorted)

	out := make([]ConstEntry, 0, len(sorted))
	usedNames := map[string]int{}
	for _, value := range sorted {
		name := prefix + toConstToken(value)
		if count, exists := usedNames[name]; exists {
			count++
			usedNames[name] = count
			name = fmt.Sprintf("%s_%d", name, count)
		} else {
			usedNames[name] = 0
		}
		out = append(out, ConstEntry{Name: name, Value: value})
	}
	return out
}

func toConstToken(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	prevUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if r >= 'a' && r <= 'z' {
				r = r - 'a' + 'A'
			}
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}
		if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	token := strings.Trim(b.String(), "_")
	if token == "" {
		return "UNSPECIFIED"
	}
	if token[0] >= '0' && token[0] <= '9' {
		return "N_" + token
	}
	return token
}

func parseOpenAPISpecFile(filePath string) (string, []string, map[endpointKey]parsedEndpoint, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("read file: %w", err)
	}

	var spec OpenAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return "", nil, nil, fmt.Errorf("parse YAML: %w", err)
	}

	version := strings.TrimSpace(spec.Info.Version)
	if version == "" {
		return "", nil, nil, fmt.Errorf("info.version is missing")
	}

	out := make(map[endpointKey]parsedEndpoint)
	serverPrefixes := extractServerPrefixes(spec.Servers)

	// Deterministic iteration
	paths := make([]string, 0, len(spec.Paths))
	for p := range spec.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		pathItem := spec.Paths[path]
		pathXRes := getString(pathItem, keyXResource)
		pathXDep := getString(pathItem, keyXDeprecatedAt)

		for _, method := range httpMethods {
			opRaw, ok := pathItem[method]
			if !ok {
				continue
			}
			op, ok := opRaw.(map[string]any)
			if !ok {
				continue
			}

			opID := getString(op, keyOperationID)
			if opID == "" {
				continue
			}

			rbac := getMap(op, keyXRBAC)

			// Resource precedence:
			// op.x-resource > op.x-rbac.resource > path.x-resource
			resource := getString(op, keyXResource)
			if resource == "" {
				resource = getString(rbac, keyResource)
			}
			if resource == "" {
				resource = pathXRes
			}

			// Action precedence:
			// op.x-rbac.action > inferred
			action := getString(rbac, keyAction)
			if action == "" {
				action = inferActionFromMethod(method, path)
			}

			// DeprecatedAt precedence:
			// op.x-deprecated-at > path.x-deprecated-at
			dep := getString(op, keyXDeprecatedAt)
			if dep == "" {
				dep = pathXDep
			}

			key := endpointKey{Method: strings.ToUpper(method), URLPattern: path}
			out[key] = parsedEndpoint{
				OperationID:  opID,
				Resource:     resource,
				Action:       action,
				Version:      version,
				DeprecatedAt: parseDeprecatedAt(dep),
			}
		}
	}

	return version, serverPrefixes, out, nil
}

func isHigherPriorityVersion(version1, version2 string) bool {
	ai, as, an := versionKey(version1)
	bi, bs, bn := versionKey(version2)
	if ai != bi {
		return ai > bi
	}
	if as != bs {
		return as < bs
	}
	return an > bn
}

func processAllSpecs(files []string) ([]EndpointOutput, []string, []ConstEntry, []ConstEntry, error) {
	byKey := map[endpointKey][]parsedEndpoint{}
	serverPrefixSet := map[string]struct{}{}
	resourceSet := map[string]struct{}{}
	actionSet := map[string]struct{}{}

	for _, f := range files {
		_, serverPrefixes, eps, err := parseOpenAPISpecFile(f)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("processing %s: %w", f, err)
		}
		for _, prefix := range serverPrefixes {
			serverPrefixSet[prefix] = struct{}{}
		}
		for k, ep := range eps {
			byKey[k] = append(byKey[k], ep)
		}
	}

	keys := make([]endpointKey, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].URLPattern != keys[j].URLPattern {
			return keys[i].URLPattern < keys[j].URLPattern
		}
		return keys[i].Method < keys[j].Method
	})

	out := make([]EndpointOutput, 0, len(keys))
	for _, k := range keys {
		versions := byKey[k]
		first := versions[0]

		// Verify all versions have consistent metadata
		for _, v := range versions[1:] {
			if v.OperationID != first.OperationID {
				log.Fatalf("Metadata mismatch for %s %s: OperationID differs between %s (%q) and %s (%q)",
					k.Method, k.URLPattern, first.Version, first.OperationID, v.Version, v.OperationID)
			}
			if v.Resource != first.Resource {
				log.Fatalf("Metadata mismatch for %s %s: Resource differs between %s (%q) and %s (%q)",
					k.Method, k.URLPattern, first.Version, first.Resource, v.Version, v.Resource)
			}
			if v.Action != first.Action {
				log.Fatalf("Metadata mismatch for %s %s: Action differs between %s (%q) and %s (%q)",
					k.Method, k.URLPattern, first.Version, first.Action, v.Version, v.Action)
			}
		}

		// Build version list sorted by priority (stable > beta > alpha)
		vout := make([]EndpointMetadataVersion, 0, len(versions))
		for _, v := range versions {
			vout = append(vout, EndpointMetadataVersion{
				Version:      v.Version,
				DeprecatedAt: v.DeprecatedAt,
			})
		}
		sort.Slice(vout, func(i, j int) bool {
			return isHigherPriorityVersion(vout[i].Version, vout[j].Version)
		})

		out = append(out, EndpointOutput{
			URLPattern:  k.URLPattern,
			Method:      k.Method,
			OperationID: first.OperationID,
			Resource:    first.Resource,
			Action:      first.Action,
			Versions:    vout,
		})

		if first.Resource != "" {
			resourceSet[first.Resource] = struct{}{}
		}
		if first.Action != "" {
			actionSet[first.Action] = struct{}{}
		}
	}

	serverPrefixes := dedupeAndSortPrefixes(serverPrefixSet)
	if len(serverPrefixes) == 0 {
		serverPrefixes = []string{"/"}
	}

	return out, serverPrefixes, buildConstEntries("API_RESOURCE_", resourceSet), buildConstEntries("API_ACTION_", actionSet), nil
}

const registryTemplate = `// Code generated by api-metadata-extractor. DO NOT EDIT.

package {{.Package}}

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// EndpointMetadataVersion contains version-specific information for an endpoint
type EndpointMetadataVersion struct {
	Version      string     // e.g., "v1", "v1beta1"
	DeprecatedAt *time.Time // nil if not deprecated; interpreted as 00:00:00 UTC
}

// EndpointMetadata contains metadata for an API endpoint
type EndpointMetadata struct {
	OperationID string
	Resource    string                    // empty = fixed-contract
	Action      string                    // x-rbac.action, else inferred from method/pattern
	Versions    []EndpointMetadataVersion // Ordered by preference (stable > beta > alpha)
}

{{- if .ResourceConsts}}
const (
{{- range .ResourceConsts}}
	{{.Name}} = {{printf "%q" .Value}}
{{- end}}
)

{{- end}}
{{- if .ActionConsts}}
const (
{{- range .ActionConsts}}
	{{.Name}} = {{printf "%q" .Value}}
{{- end}}
)

{{- end}}
// timePtr is a helper to create time.Time pointers
func timePtr(year, month, day int) *time.Time {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return &t
}

// ServerURLPrefixes lists normalized OpenAPI server URL path prefixes.
var ServerURLPrefixes = []string{
{{- range .ServerPrefixes}}
	{{printf "%q" .}},
{{- end}}
}

// APIMetadataMap provides O(1) lookup for endpoint metadata using pattern+method as key
var APIMetadataMap = map[string]EndpointMetadata{
{{- range .Endpoints}}
	{{printf "%q" (print .Method ":" .URLPattern)}}: {
		OperationID: {{printf "%q" .OperationID}},
		Resource:    {{printf "%q" .Resource}},
		Action:      {{printf "%q" .Action}},
		Versions: []EndpointMetadataVersion{
{{- range .Versions}}
			{Version: {{printf "%q" .Version}}, DeprecatedAt: {{if .DeprecatedAt}}timePtr({{.DeprecatedAt.Year}}, {{printf "%d" .DeprecatedAt.Month}}, {{.DeprecatedAt.Day}}){{else}}nil{{end}}},
{{- end}}
		},
	},
{{- end}}
}

// GetEndpointMetadata returns endpoint metadata even when routes are mounted under
// a base prefix like /api/v1 by normalizing and matching request paths.
func GetEndpointMetadata(r *http.Request) (*EndpointMetadata, bool) {
	rctx := chi.RouteContext(r.Context())
	if rctx != nil {
		if metadata, ok := lookupMetadata(r.Method, rctx.RoutePath); ok {
			return metadata, true
		}
	}

	return lookupMetadata(r.Method, r.URL.Path)
}

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

func lookupMetadata(method, path string) (*EndpointMetadata, bool) {
	candidates := normalizeCandidates(path)
	if len(candidates) == 0 {
		return nil, false
	}
	for _, candidate := range candidates {
		key := method + ":" + candidate
		if metadata, exists := APIMetadataMap[key]; exists {
			return &metadata, true
		}
	}

	methodPrefix := method + ":"
	for key, metadata := range APIMetadataMap {
		if !strings.HasPrefix(key, methodPrefix) {
			continue
		}
		pattern := strings.TrimPrefix(key, methodPrefix)
		for _, candidate := range candidates {
			if matchTemplatePath(pattern, candidate) {
				return &metadata, true
			}
		}
	}
	return nil, false
}

func normalizeCandidates(path string) []string {
	base := normalizePath(path)
	candidates := make([]string, 0, 1+len(ServerURLPrefixes))
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
	for _, prefix := range ServerURLPrefixes {
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

`

func main() {
	if len(os.Args) != 4 {
		log.Fatal("Usage: api-metadata-extractor <glob-pattern> <output-file> <package-name>")
	}
	globPattern, outputFile, packageName := os.Args[1], os.Args[2], os.Args[3]

	files, err := filepath.Glob(globPattern)
	if err != nil {
		log.Fatalf("Invalid glob pattern: %v", err)
	}
	if len(files) == 0 {
		log.Fatalf("No files match pattern: %s", globPattern)
	}

	// Sort files for deterministic processing
	sort.Strings(files)

	fmt.Printf("Processing %d OpenAPI spec file(s):\n", len(files))
	for _, f := range files {
		fmt.Printf("  - %s\n", f)
	}

	// Process all specs and merge endpoints
	endpoints, serverPrefixes, resourceConsts, actionConsts, err := processAllSpecs(files)
	if err != nil {
		log.Fatalf("Failed to process specs: %v", err)
	}

	// Generate the registry file
	tmpl, err := template.New("registry").Parse(registryTemplate)
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}

	// Ensure output directory exists
	if dir := filepath.Dir(outputFile); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Failed to create output directory: %v", err)
		}
	}

	file, err := os.Create(outputFile)
	if err != nil {
		log.Fatalf("Failed to create output file: %v", err)
	}
	defer file.Close()

	templateData := TemplateData{
		Package:        packageName,
		Endpoints:      endpoints,
		ServerPrefixes: serverPrefixes,
		ResourceConsts: resourceConsts,
		ActionConsts:   actionConsts,
	}

	if err := tmpl.Execute(file, templateData); err != nil {
		log.Fatalf("Failed to execute template: %v", err)
	}

	fmt.Printf("Generated API metadata registry with %d endpoints\n", len(endpoints))
}
