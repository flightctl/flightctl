package main

import (
	"fmt"
	"log"
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

type OpenAPISpec struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Paths map[string]map[string]any `yaml:"paths"`
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

type TemplateData struct {
	Package   string
	Endpoints []EndpointOutput
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

func parseOpenAPISpecFile(filePath string) (string, map[endpointKey]parsedEndpoint, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", nil, fmt.Errorf("read file: %w", err)
	}

	var spec OpenAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return "", nil, fmt.Errorf("parse YAML: %w", err)
	}

	version := strings.TrimSpace(spec.Info.Version)
	if version == "" {
		return "", nil, fmt.Errorf("info.version is missing")
	}

	out := make(map[endpointKey]parsedEndpoint)

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

	return version, out, nil
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

func processAllSpecs(files []string) ([]EndpointOutput, error) {
	byKey := map[endpointKey][]parsedEndpoint{}

	for _, f := range files {
		_, eps, err := parseOpenAPISpecFile(f)
		if err != nil {
			return nil, fmt.Errorf("processing %s: %w", f, err)
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
	}

	return out, nil
}

const registryTemplate = `// Code generated by api-metadata-extractor. DO NOT EDIT.

package {{.Package}}

import (
	"net/http"
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

// timePtr is a helper to create time.Time pointers
func timePtr(year, month, day int) *time.Time {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return &t
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

// GetEndpointMetadata returns metadata for a given request using the existing Chi router context
func GetEndpointMetadata(r *http.Request) (*EndpointMetadata, bool) {
	// Get the route context from the existing Chi router that already processed this request
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return nil, false
	}

	// Get the route pattern that matched in the main Chi router
	routePattern := rctx.RoutePattern()
	if routePattern == "" {
		return nil, false
	}

	// O(1) lookup using method:pattern as key
	key := r.Method + ":" + routePattern
	if metadata, exists := APIMetadataMap[key]; exists {
		return &metadata, true
	}

	return nil, false
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
	endpoints, err := processAllSpecs(files)
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
		Package:   packageName,
		Endpoints: endpoints,
	}

	if err := tmpl.Execute(file, templateData); err != nil {
		log.Fatalf("Failed to execute template: %v", err)
	}

	fmt.Printf("Generated API metadata registry with %d endpoints\n", len(endpoints))
}
