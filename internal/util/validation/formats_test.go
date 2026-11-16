package validation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateGenericName(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"a",
		"a-good-name",
		strings.Repeat("a", 63),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateGenericName(&val, "good.name"))
	}

	badValues := []string{
		"",
		"-starts-with-dash",
		"with.dots",
		"with_underscores",
		"WITH-CAPITAL-LETTERS",
		strings.Repeat("long", 16),
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateGenericName(&val, "bad.name"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateConfigName(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"a",
		"a-good-name",
		"example.json",
		"example.env",
		"config.yaml",
		"my.config.v2",
		"a.b",
		"my-app.config",
		strings.Repeat("a", 253),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateConfigName(&val, "good.config.name"), fmt.Sprintf("value: %q", val))
	}

	badValues := []string{
		"",
		".starts-with-dot",
		"ends-with-dot.",
		"-starts-with-dash",
		"ends-with-dash-",
		"WITH-CAPITAL-LETTERS",
		"has spaces",
		"has_underscores",
		strings.Repeat("a", 254),
		"ex..com",
		"a...b",
		"example..json",
		"example.-json",
		"example-.json",
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateConfigName(&val, "bad.config.name"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateOciImageReference(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"flightctl",
		"flightctl:latest",
		"flightctl:v0.0.1",
		"quay.io/flightctl",
		"quay.io/flightctl/flightctl",
		"quay.io/flightctl/flightctl:latest",
		"quay.io/flightctl/flightctl@sha256:0123456789abcdef0123456789abcdef",
		"quay.io/flightctl/flightctl:latest@sha256:0123456789abcdef0123456789abcdef",
		"weird__but_valid",
		strings.Repeat("a", 64),
		"image:" + strings.Repeat("a", 128),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateOciImageReference(&val, "good.image.ref"))
	}

	badValues := []string{
		"_underscore",
		"not.a//domain",
		"quay.io/flightctl/flightctl@sha256:0123456789abcdef0123456789abcde",
		"image:" + strings.Repeat("a", 129),
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateOciImageReference(&val, "bad.image.ref"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateGitRevision(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"main",
		"latest",
		"v1.0.0",
		"9fac431b7f4f319ead0195034064012e732bbb0c",
		"weird__but--valid..branch//name",
		strings.Repeat("a", 244),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateGitRevision(&val, "good.image.ref"))
	}

	badValues := []string{
		"contains whitespace",
		"_startswithunderscore",
		strings.Repeat("a", 245),
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateGitRevision(&val, "bad.image.ref"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateSystemdUnitPattern(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"foo.service",
		"service",
		"foo[0-9].service",
		"foo?.service",
		"foo\\.service",
	}
	for _, val := range goodValues {
		assert.Empty(ValidateSystemdName(&val, "good.unit"))
	}

	badValues := []string{
		"foo@@bar.service",
		"",
		"@bar.service",
		"foo;bar.service",
		string(make([]rune, 257)),
	}

	for _, val := range badValues {
		assert.NotEmpty(ValidateSystemdName(&val, "bad.unit"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateOciImageReferenceWithTemplates(t *testing.T) {
	assert := assert.New(t)

	// Valid template cases - should PASS
	// Note: This function requires TEMPLATED tags (not literal tags or digest templates)
	goodValues := []string{
		// Basic tag templates (required)
		"quay.io/flightctl/device:{{ .metadata.labels.version }}",
		"flightctl:{{ .metadata.labels.tag }}",
		"registry.com/ns/image:{{ getOrDefault .metadata.labels \"key\" \"default\" }}",
		// Tag templates with literal digest (templated tag + literal digest)
		"quay.io/flightctl/device:{{ .metadata.labels.version }}@sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abc123de",
		"localhost:5000/image:{{ .metadata.labels.tag }}@sha256:0123456789abcdef0123456789abcdef",
		// Multiple template parameters in tag
		"registry.com/image:{{ .metadata.labels.version }}{{ .metadata.labels.suffix }}",
		// Complex template expressions in tag
		"localhost:5000/image:{{ index .metadata.labels \"build-id\" }}",
		"registry.example.com:8080/namespace/image:{{ getOrDefault .metadata.labels \"version\" \"latest\" }}",
		// Templates with spaces INSIDE the template (which is valid Go template syntax)
		"quay.io/x:{{ getOrDefault .metadata.labels \"key\" \"default\" }}",
		"quay.io/x:{{ index .metadata.labels \"build-version\" }}",
	}
	for _, val := range goodValues {
		errors := ValidateOciImageReferenceWithTemplates(&val, "good.image.ref")
		assert.Empty(errors, fmt.Sprintf("Expected valid template reference %q to pass, but got errors: %v", val, errors))
	}

	// Invalid cases - should FAIL
	badValues := []string{
		// Spaces in static parts (main EDM-1183 bug being fixed)
		"192.168.1.144/flightctl-de vice:{{ .metadata.labels.version }}",
		"quay.io/flight ctrl/device:{{ .metadata.labels.version }}",
		"bad domain.com/image:{{ .metadata.labels.version }}",
		// Whitespace around separators
		"quay.io/x : {{ .metadata.labels.version }}", // space before colon
		"quay.io/x: {{ .metadata.labels.version }}",  // space after colon
		"quay.io/x :{{ .metadata.labels.version }}",  // space before colon
		"quay.io/x: {{.metadata.labels.version}}",    // space after colon (even without space in template)
		// Digest templates (NOT supported by CodeRabbit pattern - only tag templates allowed)
		"quay.io/x@{{ .metadata.labels.digest }}",                                // template in digest position
		"flightctl@{{ .metadata.labels.hash }}",                                  // template in digest position
		"quay.io/x:{{ .metadata.labels.version }}@{{ .metadata.labels.digest }}", // template in both tag and digest
		// Invalid patterns that CodeRabbit specifically mentioned
		"quay.io/x",  // no template tag (this function requires templated tags)
		"quay.io/x:", // empty tag (+ quantifier requires at least one template)
		"quay.io/x@sha256:abc123:{{ .metadata.labels.version }}", // invalid "...@digest:{{...}}" pattern
		"quay.io/x@sha256:abc123{{ .metadata.labels.version }}",  // digest followed by template (invalid structure)
		// Invalid OCI structure even with templates
		"_invalid:{{ .metadata.labels.version }}",          // starts with underscore
		"quay.io/UPPERCASE:{{ .metadata.labels.version }}", // uppercase in image name (not domain)
		"image..double.dot:{{ .metadata.labels.version }}", // double dots
		// Invalid template placement/structure
		"quay.io/x:tag:{{ .metadata.labels.version }}",        // double colon
		"quay.io/x@digest@{{ .metadata.labels.version }}",     // double @
		"quay.io/x:tag@digest:{{ .metadata.labels.version }}", // colon after digest
		// Multiple issues combined
		"bad domain.com/image name:tag:{{ .metadata.labels.version }}",     // spaces + double colon
		"192.168.1.144/flightctl-de vice : {{ .metadata.labels.version }}", // spaces in name + around colon
	}
	for _, val := range badValues {
		errors := ValidateOciImageReferenceWithTemplates(&val, "bad.image.ref")
		assert.NotEmpty(errors, fmt.Sprintf("Expected invalid template reference %q to fail validation", val))
	}
}

func TestSplitOuterConditional(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantGuard string
		wantBody  string
		wantOk    bool
	}{
		{
			name:   "When the reference is a plain literal it should not split",
			value:  "quay.io/org/os:v2",
			wantOk: false,
		},
		{
			name:   "When the reference is empty it should not split",
			value:  "",
			wantOk: false,
		},
		{
			name:      "When the reference is wrapped in a helper guard it should split",
			value:     `{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/os:v2",
			wantOk:    true,
		},
		{
			name:      "When the reference is wrapped in a hand-written label guard it should split",
			value:     `{{ if eq (getOrDefault .metadata.labels "feature.flightctl.io/os.mode" "image") "image" }}quay.io/org/os:v2{{ end }}`,
			wantGuard: `eq (getOrDefault .metadata.labels "feature.flightctl.io/os.mode" "image") "image"`,
			wantBody:  "quay.io/org/os:v2",
			wantOk:    true,
		},
		{
			name:      "When the guard is an arbitrary supported expression it should split",
			value:     `{{ if .metadata.labels.tier }}quay.io/org/os:v2{{ end }}`,
			wantGuard: ".metadata.labels.tier",
			wantBody:  "quay.io/org/os:v2",
			wantOk:    true,
		},
		{
			name:      "When the wrapper uses trim markers it should split without them",
			value:     `{{- if deviceHasImageMode -}}quay.io/org/os:v2{{- end -}}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/os:v2",
			wantOk:    true,
		},
		{
			name:      "When the wrapper has no inner spaces it should split",
			value:     `{{if deviceHasImageMode}}quay.io/org/os:v2{{end}}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/os:v2",
			wantOk:    true,
		},
		{
			name:      "When the wrapper mixes tight and spaced markers it should split",
			value:     `{{if deviceHasImageMode}}quay.io/org/os:v2{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/os:v2",
			wantOk:    true,
		},
		{
			name:      "When the body is a parameterized tag it should split",
			value:     `{{ if deviceHasImageMode }}quay.io/org/os:{{ .metadata.labels.version }}{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/os:{{ .metadata.labels.version }}",
			wantOk:    true,
		},
		{
			name:      "When the body is empty it should split with an empty body",
			value:     `{{ if deviceHasImageMode }}{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "",
			wantOk:    true,
		},
		{
			name:      "When the body mentions the guard helper it should return the guard without it",
			value:     `{{ if deviceHasImageMode }}quay.io/org/devicehasimagemode:v2{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/devicehasimagemode:v2",
			wantOk:    true,
		},
		{
			name:      "When the body mentions the mode label key it should return the guard without it",
			value:     `{{ if deviceHasImageMode }}quay.io/org/os:{{ .metadata.labels.version }}-os.mode{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/os:{{ .metadata.labels.version }}-os.mode",
			wantOk:    true,
		},
		{
			name:      "When the image name merely contains end it should split",
			value:     `{{ if deviceHasImageMode }}quay.io/org/legend:v2{{ end }}`,
			wantGuard: "deviceHasImageMode",
			wantBody:  "quay.io/org/legend:v2",
			wantOk:    true,
		},
		{
			name:   "When there are two sibling conditionals with spaced end markers it should not split",
			value:  `{{ if deviceHasImageMode }}quay.io/org/a:v1{{ end }}{{ if deviceHasImageMode }}quay.io/org/b:v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When there are two sibling conditionals with tight end markers it should not split",
			value:  `{{if deviceHasImageMode}}quay.io/org/a:v1{{end}}{{if deviceHasImageMode}}quay.io/org/b:v2{{end}}`,
			wantOk: false,
		},
		{
			name:   "When the wrapper has an else branch it should not split",
			value:  `{{ if deviceHasImageMode }}quay.io/org/a:v1{{ else }}quay.io/org/b:v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When the wrapper has an else if branch it should not split",
			value:  `{{ if deviceHasImageMode }}quay.io/org/a:v1{{ else if deviceHasImageMode }}quay.io/org/b:v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When the conditionals are nested it should not split",
			value:  `{{ if deviceHasImageMode }}{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When nested conditionals use trim markers it should not split",
			value:  `{{- if deviceHasImageMode -}}{{- if deviceHasImageMode -}}quay.io/org/os:v2{{- end -}}{{- end -}}`,
			wantOk: false,
		},
		{
			name:   "When the wrapper is unterminated it should not split",
			value:  `{{ if deviceHasImageMode }}quay.io/org/os:v2`,
			wantOk: false,
		},
		{
			name:   "When there is only an end marker it should not split",
			value:  `{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When a literal precedes the wrapper it should not split",
			value:  `quay.io/org/os:{{ if deviceHasImageMode }}v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When a literal follows the wrapper it should not split",
			value:  `{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}-suffix`,
			wantOk: false,
		},
		{
			name:   "When the wrapper is a range it should not split",
			value:  `{{ range .metadata.labels }}quay.io/org/os:v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When the wrapper is a with it should not split",
			value:  `{{ with .metadata.labels }}quay.io/org/os:v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When the if keyword is only a prefix of another identifier it should not split",
			value:  `{{ ifdef deviceHasImageMode }}quay.io/org/os:v2{{ end }}`,
			wantOk: false,
		},
		{
			name:   "When the body spans multiple lines it should not split",
			value:  "{{ if deviceHasImageMode }}quay.io/org/\nos:v2{{ end }}",
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			guard, body, ok := SplitOuterConditional(tt.value)
			assert.Equal(tt.wantOk, ok)
			assert.Equal(tt.wantGuard, guard)
			assert.Equal(tt.wantBody, body)
		})
	}
}
