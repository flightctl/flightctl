// Package openapi loads OpenAPI documents that are compiled into the binary
// with go:embed.
//
// API packages embed their openapi.yaml verbatim and wrap it in a Document.
// Embedding the YAML rather than a generated, base64-encoded blob keeps the
// specification readable and diffable in source control, at the cost of
// parsing YAML at runtime instead of JSON. Document pays that cost at most
// once per process.
//
// That trade is deliberate. Parsing YAML is slower than decoding the blob was
// (roughly 125ms versus 75ms for core v1beta1), and specifications that reuse
// another version's schemas pay more still, because the blob used to arrive
// with its external references already inlined whereas we now resolve them by
// parsing the referenced document too. Every call after the first is a cached
// pointer read of a few nanoseconds, so the whole cost lands once during
// process start-up and never on a request path.
package openapi

import (
	"fmt"
	"net/url"
	"path"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

// Document is an OpenAPI document embedded in a Go package, parsed lazily.
//
// The first call to Spec parses the document; every later call returns the
// cached result, including a cached failure. A Document is safe for
// concurrent use.
type Document struct {
	doc          []byte
	externalRefs map[string][]byte

	once sync.Once
	spec *openapi3.T
	err  error
}

// NewDocument wraps a raw OpenAPI document, normally one populated by an
// embed directive.
//
// externalRefs resolves the document's external "$ref" values. Each key is a
// reference path exactly as it appears in the document (for example
// "../../core/v1beta1/openapi.yaml") and each value is the raw document it
// names, typically obtained from the Go package that embeds it. A document
// with no external references passes nil. References are resolved from these
// bytes alone: the filesystem is never consulted, so a reference that is
// missing from the map is an error rather than a disk read, and URL-based
// references are not supported.
//
// Only directly reachable references need an entry. Resolving a reference
// whose target itself has external references requires those nested paths to
// be present in the map as well, keyed as the nested document spells them.
//
// NewDocument does not parse or copy doc. Neither doc nor the values in
// externalRefs may be modified afterwards.
func NewDocument(doc []byte, externalRefs map[string][]byte) *Document {
	return &Document{doc: doc, externalRefs: externalRefs}
}

// Raw returns the embedded document as it appears in source control.
//
// It is exported so a package whose specification refers to this one can pass
// the bytes to NewDocument as an external reference. Callers must not modify
// the returned slice.
func (d *Document) Raw() []byte {
	return d.doc
}

// Spec returns the parsed document, parsing it on the first call.
//
// Every caller receives the same *openapi3.T. Treat it as read-only: a caller
// that needs to adjust the document must copy it first, or it will change
// what every other caller sees.
func (d *Document) Spec() (*openapi3.T, error) {
	d.once.Do(d.load)
	return d.spec, d.err
}

func (d *Document) load() {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, uri *url.URL) ([]byte, error) {
		ref := path.Clean(uri.String())
		doc, ok := d.externalRefs[ref]
		if !ok {
			return nil, fmt.Errorf("unresolvable external OpenAPI reference %q: no such embedded document", ref)
		}
		return doc, nil
	}

	spec, err := loader.LoadFromData(d.doc)
	if err != nil {
		d.err = fmt.Errorf("loading embedded OpenAPI document: %w", err)
		return
	}
	d.spec = spec
}
