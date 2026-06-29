package main

import (
	"encoding/json"
	"fmt"
	"os"

	pkgmanifest "github.com/flightctl/flightctl/scripts/air-gap/pkg/manifest"
)

// loadBuildManifest returns the mirror manifest embedded in the binary.
//
// If MIRROR_MANIFEST is set it is treated as a path to a JSON file that
// replaces the embedded manifest entirely.  This is the escape hatch for
// custom image configurations or pre-staging images for an upcoming upgrade
// without reinstalling the tool.
func loadBuildManifest() (*pkgmanifest.Build, error) {
	data := embeddedBuildManifest
	if path := os.Getenv("MIRROR_MANIFEST"); path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("MIRROR_MANIFEST=%s: %w", path, err)
		}
		logInfo("  Override: MIRROR_MANIFEST → %s", path)
	}

	var m pkgmanifest.Build
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse mirror manifest: %w", err)
	}
	if m.SchemaVersion != pkgmanifest.CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported manifest schema version %q (expected %q)",
			m.SchemaVersion, pkgmanifest.CurrentSchemaVersion)
	}
	return &m, nil
}

// resolveVariant looks up variant in the manifest and returns the resolved
// image pairs and RPM list.
//
// For each image: if the manifest Tag is non-empty it is used as-is (the image
// has a pinned version from a third-party source).  If Tag is empty,
// effectiveTag (AppVersion or --tag-override) is applied so flightctl service
// images are mirrored at the correct release tag.
func resolveVariant(m *pkgmanifest.Build, variant, effectiveTag string) ([]ImagePair, []string, error) {
	v, ok := m.Variants[variant]
	if !ok {
		return nil, nil, fmt.Errorf("variant %q not found in embedded manifest — "+
			"is this a supported variant? (%s)", variant, supportedVariantList(m))
	}

	pairs := make([]ImagePair, 0, len(v.Images))
	for _, img := range v.Images {
		tag := img.Tag
		if tag == "" || tag == "latest" {
			tag = effectiveTag
		}
		pairs = append(pairs, ImagePair{
			Source: img.Ref + ":" + tag,
			Dest:   ImageToDest(img.Ref, tag),
		})
	}

	return Dedup(pairs), v.RPMs, nil
}

// supportedVariantList returns a human-readable comma-separated list of
// variant names present in the manifest.
func supportedVariantList(m *pkgmanifest.Build) string {
	names := make([]string, 0, len(m.Variants))
	for k := range m.Variants {
		names = append(names, k)
	}
	return fmt.Sprintf("%v", names)
}
