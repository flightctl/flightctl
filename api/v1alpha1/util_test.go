package v1alpha1

import (
	"fmt"
	"testing"
	"text/template"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/stretchr/testify/require"
)

func TestExecuteGoTemplateOnDevice(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name        string
		paramString string
		err         bool
		expect      string
	}{
		{
			name:        "no parameters",
			paramString: "hello world",
			err:         false,
			expect:      "hello world",
		},
		{
			name:        "simple name access",
			paramString: "hello {{ .metadata.name }} world",
			err:         false,
			expect:      "hello Name world",
		},
		{
			name:        "name access using Go struct syntax fails",
			paramString: "hello {{ .Metadata.Name }} world",
			err:         true,
		},
		{
			name:        "label access using Go struct syntax fails",
			paramString: "hello {{ .Metadata.Labels.key }} world",
			err:         true,
		},
		{
			name:        "accessing non-exposed field fails",
			paramString: "hello {{ .metadata.annotations.key }} world",
			err:         true,
		},
		{
			name:        "upper name",
			paramString: "Hello {{ upper .metadata.name }}",
			err:         false,
			expect:      "Hello NAME",
		},
		{
			name:        "upper label",
			paramString: "Hello {{ upper .metadata.labels.key }}",
			err:         false,
			expect:      "Hello VALUE",
		},
		{
			name:        "lower name",
			paramString: "Hello {{ lower .metadata.name }}",
			err:         false,
			expect:      "Hello name",
		},
		{
			name:        "lower label",
			paramString: "Hello {{ lower .metadata.labels.key }}",
			err:         false,
			expect:      "Hello value",
		},
		{
			name:        "replace name",
			paramString: "Hello {{ replace \"N\" \"G\" .metadata.name }}",
			err:         false,
			expect:      "Hello Game",
		},
		{
			name:        "replace label",
			paramString: "Hello {{ replace \"Va\" \"b\" .metadata.labels.key }}",
			err:         false,
			expect:      "Hello blue",
		},
		{
			name:        "index",
			paramString: "Hello {{ index .metadata.labels \"key\" }}",
			err:         false,
			expect:      "Hello Value",
		},
		{
			name:        "pipeline found key",
			paramString: "Hello {{ .metadata.labels.key | upper | replace \"VA\" \"B\"}}",
			err:         false,
			expect:      "Hello BLUE",
		},
		{
			name:        "pipeline default key not found",
			paramString: "Hello {{ getOrDefault .metadata.labels \"otherkey\" \"DEFAULT\" | lower | replace \"de\" \"my\"}}",
			err:         false,
			expect:      "Hello myfault",
		},
		{
			name:        "pipeline default key found",
			paramString: "Hello {{ getOrDefault .metadata.labels \"key\" \"DEFAULT\" | lower | replace \"de\" \"my\"}}",
			err:         false,
			expect:      "Hello value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := fmt.Sprintf("test: %s", tt.name)
			tmpl, err := template.New("t").Option("missingkey=error").Funcs(GetGoTemplateFuncMap()).Parse(tt.paramString)
			require.NoError(err, msg)

			dev := &Device{
				Metadata: ObjectMeta{
					Name:   util.StrToPtr("Name"),
					Labels: &map[string]string{"key": "Value"},
				},
			}
			output, err := ExecuteGoTemplateOnDevice(tmpl, dev)
			if tt.err {
				require.Error(err, msg)
			} else {
				require.NoError(err, msg)
				require.Equal(tt.expect, output, msg)
			}
		})
	}
}
