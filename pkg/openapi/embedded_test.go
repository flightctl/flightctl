package openapi

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const baseDoc = `
openapi: 3.0.3
info:
  title: base
  version: v1
paths: {}
components:
  schemas:
    Widget:
      type: object
      required: [name]
      properties:
        name:
          type: string
`

const refDoc = `
openapi: 3.0.3
info:
  title: ref
  version: v1
paths:
  /widgets:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: '../base/openapi.yaml#/components/schemas/Widget'
`

func TestDocumentSpec(t *testing.T) {
	tests := []struct {
		name         string
		doc          string
		externalRefs map[string][]byte
		wantErr      string
	}{
		{
			name: "When a document has no external references it should parse",
			doc:  baseDoc,
		},
		{
			name:         "When an external reference is registered it should resolve",
			doc:          refDoc,
			externalRefs: map[string][]byte{"../base/openapi.yaml": []byte(baseDoc)},
		},
		{
			name:    "When an external reference is not registered it should report the missing path",
			doc:     refDoc,
			wantErr: `unresolvable external OpenAPI reference "../base/openapi.yaml"`,
		},
		{
			name:    "When the document is not valid YAML it should report a load failure",
			doc:     "\topenapi: [",
			wantErr: "loading embedded OpenAPI document",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := NewDocument([]byte(tt.doc), tt.externalRefs).Spec()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, spec)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, spec)
		})
	}
}

func TestDocumentSpecResolvesExternalRefValue(t *testing.T) {
	d := NewDocument([]byte(refDoc), map[string][]byte{"../base/openapi.yaml": []byte(baseDoc)})
	spec, err := d.Spec()
	require.NoError(t, err)

	schema := spec.Paths.Find("/widgets").Get.Responses.Status(200).Value.
		Content["application/json"].Schema
	require.NotNil(t, schema.Value, "external $ref must be resolved to a value")
	require.Equal(t, []string{"name"}, schema.Value.Required)
}

func TestDocumentSpecParsesOnceAndIsConcurrencySafe(t *testing.T) {
	d := NewDocument([]byte(baseDoc), nil)

	const goroutines = 16
	specs := make([]any, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spec, err := d.Spec()
			require.NoError(t, err)
			specs[i] = spec
		}()
	}
	wg.Wait()

	for i := range goroutines {
		require.Same(t, specs[0], specs[i], "every caller must share the one cached spec")
	}
}

func TestDocumentRawReturnsEmbeddedBytes(t *testing.T) {
	require.Equal(t, []byte(baseDoc), NewDocument([]byte(baseDoc), nil).Raw())
}

// BenchmarkDocumentSpecFirstParse measures the one-time cost a process pays to
// parse an embedded document, i.e. the cost sync.Once amortises away.
func BenchmarkDocumentSpecFirstParse(b *testing.B) {
	doc := []byte(baseDoc)
	for b.Loop() {
		if _, err := NewDocument(doc, nil).Spec(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDocumentSpecCached measures the steady-state cost of every call
// after the first.
func BenchmarkDocumentSpecCached(b *testing.B) {
	d := NewDocument([]byte(baseDoc), nil)
	if _, err := d.Spec(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := d.Spec(); err != nil {
			b.Fatal(err)
		}
	}
}
