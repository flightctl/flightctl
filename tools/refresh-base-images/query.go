package main

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// runQuery implements the "query" subcommand. It reads a field value from
// images.yaml and prints it to stdout. This replaces hack/yaml-field.sh
// with a proper Go implementation that reuses the existing YAML parser.
//
// Usage:
//
//	refresh-base-images query --file <path> --key <container> --field <field>
//
// Exit codes: 0 on success, 1 if the key or field is not found or on error.
func runQuery(args []string) int {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	file := fs.String("file", "", "Path to images.yaml file")
	key := fs.String("key", "", "Top-level container name (e.g. api, worker)")
	field := fs.String("field", "", "Field name to read (e.g. build_base, run_base, image, tag)")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *file == "" || *key == "" || *field == "" {
		fmt.Fprintln(os.Stderr, "Usage: refresh-base-images query --file <path> --key <container> --field <field>")
		return 1
	}

	value, err := queryField(*file, *key, *field)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}

	fmt.Print(value)
	return 0
}

// queryField reads a single field value from an images.yaml file.
// It uses ReadImagesYAML for parsing but falls back to the raw YAML
// node tree to support all fields (not just build_base/run_base).
func queryField(path, key, field string) (string, error) {
	entries, doc, err := ReadImagesYAML(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	// Check the key exists.
	if _, ok := entries[key]; !ok {
		return "", fmt.Errorf("key %q not found in %s", key, path)
	}

	// For fields tracked in ImageEntry (build_base, run_base), use the
	// struct directly for type safety.
	if val, ok := getStructField(entries[key], field); ok && val != "" {
		return val, nil
	}

	// Fall back to the raw YAML node tree for fields not in ImageEntry
	// (e.g. image, tag).
	val, err := getNodeField(doc, key, field)
	if err != nil {
		return "", err
	}
	return val, nil
}

// getStructField returns the value of a YAML-tagged field on ImageEntry.
func getStructField(entry ImageEntry, yamlTag string) (string, bool) {
	t := reflect.TypeOf(entry)
	v := reflect.ValueOf(entry)
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		// Strip ",omitempty" etc.
		if idx := strings.Index(tag, ","); idx != -1 {
			tag = tag[:idx]
		}
		if tag == yamlTag {
			return v.Field(i).String(), true
		}
	}
	return "", false
}

// getNodeField reads a scalar field value from the raw YAML node tree.
func getNodeField(doc *yaml.Node, key, field string) (string, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return "", fmt.Errorf("invalid document structure")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("expected mapping node at root")
	}

	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		containerMap := root.Content[i+1]
		if containerMap.Kind != yaml.MappingNode {
			return "", fmt.Errorf("key %q is not a mapping", key)
		}
		for j := 0; j < len(containerMap.Content)-1; j += 2 {
			if containerMap.Content[j].Value == field {
				return containerMap.Content[j+1].Value, nil
			}
		}
		return "", fmt.Errorf("field %q not found under %q", field, key)
	}

	return "", fmt.Errorf("key %q not found", key)
}
