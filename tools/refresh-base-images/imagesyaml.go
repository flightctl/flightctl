package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrBaseImageNotFound is returned when no images.yaml entry references
// a given base image. Callers can use errors.Is to distinguish "not
// configured" from real read/parse errors.
var ErrBaseImageNotFound = errors.New("base image not found")

// ImageEntry represents a single container entry in images.yaml.
// Only fields relevant to base image management are defined here;
// unknown keys are preserved by operating on the raw YAML node tree.
//
// BuildBase and RunBase hold full image references including the tag,
// e.g. "registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323".
// Use SplitImageRef to separate image path from tag.
type ImageEntry struct {
	BuildBase string `yaml:"build_base,omitempty"`
	RunBase   string `yaml:"run_base,omitempty"`
}

// ImagesYAML is the top-level structure: a map of container name to ImageEntry.
type ImagesYAML map[string]ImageEntry

// SplitImageRef splits a full image reference into image path and tag
// by splitting on the last colon. For example:
//
//	"registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323"
//	  → ("registry.access.redhat.com/ubi10/go-toolset", "1.26.7-1787775323")
//
// Returns the full ref as the image and "" as tag if no colon is found.
func SplitImageRef(ref string) (image, tag string) {
	idx := strings.LastIndex(ref, ":")
	if idx < 0 {
		return ref, ""
	}
	return ref[:idx], ref[idx+1:]
}

// ReadImagesYAML reads and parses an images.yaml file.
// It returns the parsed entries (only base-image fields) along with
// the raw YAML node tree for round-trip editing.
func ReadImagesYAML(path string) (ImagesYAML, *yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Parse into structured map for easy field access.
	var entries ImagesYAML
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	// Also parse into raw node tree for round-trip editing.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parsing %s node tree: %w", path, err)
	}

	return entries, &doc, nil
}

// WriteImagesYAML writes the YAML node tree back to disk, preserving
// comments and formatting with consistent 2-space indentation.
func WriteImagesYAML(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encoding: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("closing encoder: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// UpdateNodeField sets a scalar field value in the YAML node tree.
// It finds the top-level key `container`, then the nested key `field`,
// and updates its value.
func UpdateNodeField(doc *yaml.Node, container, field, value string) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("expected document node")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node at root")
	}

	// Find the container key.
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == container {
			containerMap := root.Content[i+1]
			if containerMap.Kind != yaml.MappingNode {
				return fmt.Errorf("container %q is not a mapping", container)
			}
			// Find the field key within the container mapping.
			for j := 0; j < len(containerMap.Content)-1; j += 2 {
				if containerMap.Content[j].Value == field {
					containerMap.Content[j+1].Value = value
					// Use plain style for combined image:tag refs.
					containerMap.Content[j+1].Tag = "!!str"
					containerMap.Content[j+1].Style = 0
					return nil
				}
			}
			// Field not found — add it.
			containerMap.Content = append(containerMap.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: field},
				&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str", Style: 0},
			)
			return nil
		}
	}

	return fmt.Errorf("container %q not found", container)
}

// GetCurrentTagForImage returns the current tag for a specific image
// from images.yaml entries. It searches all entries for one whose
// build_base or run_base starts with the given image prefix.
// Returns an error if multiple entries reference the same image prefix
// with different tags, since map iteration order would make the result
// nondeterministic.
func GetCurrentTagForImage(entries ImagesYAML, imagePrefix string) (string, error) {
	var found string
	for name, entry := range entries {
		for _, ref := range []string{entry.BuildBase, entry.RunBase} {
			img, tag := SplitImageRef(ref)
			if img != imagePrefix || tag == "" {
				continue
			}
			if found == "" {
				found = tag
			} else if found != tag {
				return "", fmt.Errorf("conflicting tags for %q: container %q has %q but another has %q",
					imagePrefix, name, tag, found)
			}
		}
	}
	if found == "" {
		return "", fmt.Errorf("no entry with base image %q: %w", imagePrefix, ErrBaseImageNotFound)
	}
	return found, nil
}

// ContainersWithBase returns the names of containers that use the given
// image as their build_base or run_base, along with the field name
// ("build_base" or "run_base") to update.
func ContainersWithBase(entries ImagesYAML, imagePrefix string) []struct {
	Name  string
	Field string
} {
	var result []struct {
		Name  string
		Field string
	}
	for name, entry := range entries {
		if img, _ := SplitImageRef(entry.BuildBase); img == imagePrefix {
			result = append(result, struct {
				Name  string
				Field string
			}{Name: name, Field: "build_base"})
		}
		if img, _ := SplitImageRef(entry.RunBase); img == imagePrefix {
			result = append(result, struct {
				Name  string
				Field string
			}{Name: name, Field: "run_base"})
		}
	}
	return result
}

// ImagesYAMLPath returns the path to images.yaml for a given EL version.
func ImagesYAMLPath(imagesDir, ver string) string {
	return filepath.Join(imagesDir, fmt.Sprintf("el%s", ver), "images.yaml")
}
