package client

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
)

const (
	QuadletProjectLabelKey = "io.flightctl.quadlet.project"

	dropinExtension = ".conf"
)

// mergeDropins loads and merges drop-in .conf files into the base unit.
func mergeDropins(reader fileio.Reader, baseDir string, filename string, unit *quadlet.Unit) error {
	dropins := quadlet.DropinDirectories(filename)
	dropinDirs := make([]string, 0, len(dropins))
	for _, dropinPath := range dropins {
		dropinDirs = append(dropinDirs, filepath.Join(baseDir, dropinPath))
	}

	dropinFiles := make(map[string]string)
	var dropinNames []string
	for _, dropinDir := range dropinDirs {
		entries, err := reader.ReadDir(dropinDir)
		// drop in dirs don't have to exist, but if they do, they must be processed
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("reading drop-in dir %q: %w", dropinDir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			dropinName := entry.Name()
			if filepath.Ext(dropinName) != dropinExtension {
				continue
			}

			// dropin dirs are processed in order of specificity. Therefore,
			// if a drop in with the same name has already been found, the current
			// one must be ignored.
			if _, ok := dropinFiles[dropinName]; ok {
				continue
			}

			dropinFiles[dropinName] = filepath.Join(dropinDir, dropinName)
			dropinNames = append(dropinNames, dropinName)
		}
	}

	if len(dropinFiles) == 0 {
		return nil
	}

	// sort the names in ascending order so that the highest priority items are applied
	// last which allows for proper overriding
	sort.Strings(dropinNames)
	for _, dropinName := range dropinNames {
		dropinPath := dropinFiles[dropinName]

		content, err := reader.ReadFile(dropinPath)
		if err != nil {
			return fmt.Errorf("reading drop-in file %q: %w", dropinPath, err)
		}

		dropIn, err := quadlet.NewUnit(content)
		if err != nil {
			return fmt.Errorf("parsing drop-in file %q: %w", dropinPath, err)
		}

		unit.Merge(dropIn)
	}

	return nil
}

// ParseQuadletReferencesFromSpec parses Quadlet specifications from a slice of inline application content.
// Drop-in overrides are applied to ensure that the specs
// It returns a map where the key is the filename and the value is the parsed QuadletSpec.
func ParseQuadletReferencesFromSpec(contents []v1beta1.ApplicationContent) (map[string]*common.QuadletReferences, error) {
	baseFiles := make(map[string][]byte)
	dropinFiles := make(map[string]map[string][]byte)

	for _, c := range contents {
		filename := c.Path
		if filename == "" {
			continue
		}

		contentBytes, err := c.ContentsDecoded()
		if err != nil {
			return nil, fmt.Errorf("decoding content %q: %w", filename, err)
		}

		ext := filepath.Ext(filename)
		if _, ok := common.SupportedQuadletExtensions[ext]; ok {
			baseFiles[filename] = contentBytes
		} else if ext == dropinExtension {
			// treat all .conf files as dropins for simplicity
			// when processed later, only .conf files that are in dropin directories
			// will actually be processed
			dir := filepath.Dir(filename)
			if _, ok := dropinFiles[dir]; !ok {
				dropinFiles[dir] = make(map[string][]byte)
			}
			dropinFiles[dir][filepath.Base(filename)] = contentBytes
		}
	}

	if len(baseFiles) == 0 {
		return nil, fmt.Errorf("%w: in app spec", errors.ErrNoQuadletFile)
	}

	quadlets := make(map[string]*common.QuadletReferences)

	// process dropins to ensure all images are retrieved properly
	for filename, content := range baseFiles {
		baseUnit, err := quadlet.NewUnit(content)
		if err != nil {
			return nil, fmt.Errorf("deserializing content %q: %w", filename, err)
		}

		dropinDirs := quadlet.DropinDirectories(filename)
		var dropinNames []string
		// map of dropin file to the contents of the drop in.
		dropinContents := make(map[string][]byte)

		// once a dropin is discovered, any other dropins matching the same name will be ignored
		for _, dropinDir := range dropinDirs {
			if dropins, ok := dropinFiles[dropinDir]; ok {
				for name, data := range dropins {
					if _, ok = dropinContents[name]; !ok {
						dropinNames = append(dropinNames, name)
						dropinContents[name] = data
					}
				}
			}

		}
		// sort the names in ascending order so that the highest priority items are applied
		// last which allows for proper overriding
		sort.Strings(dropinNames)
		for _, dropinName := range dropinNames {
			dropinContent := dropinContents[dropinName]
			dropin, err := quadlet.NewUnit(dropinContent)
			if err != nil {
				return nil, fmt.Errorf("deserializing drop-in content %q: %w", dropinName, err)
			}

			baseUnit.Merge(dropin)
		}

		mergedContents, err := baseUnit.Write()
		if err != nil {
			return nil, fmt.Errorf("serializing merged quadlet: %w", err)
		}
		spec, err := common.ParseQuadletReferences(mergedContents)
		if err != nil {
			return nil, fmt.Errorf("parsing quadlet spec from %q: %w", filename, err)
		}

		quadlets[filename] = spec
	}

	return quadlets, nil
}

// ParseQuadletReferencesFromDir reads quadlet specs from the given directory.
// It returns a map where the key is the filename and the value is the parsed QuadletSpec.
func ParseQuadletReferencesFromDir(reader fileio.Reader, dir string) (map[string]*common.QuadletReferences, error) {
	quadlets := make(map[string]*common.QuadletReferences)

	entries, err := reader.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		ext := filepath.Ext(filename)

		if _, ok := common.SupportedQuadletExtensions[ext]; !ok {
			continue
		}

		filePath := filepath.Join(dir, filename)
		content, err := reader.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("reading quadlet file %q: %w", filePath, err)
		}

		baseUnit, err := quadlet.NewUnit(content)
		if err != nil {
			return nil, fmt.Errorf("deserializing quadlet file %q: %w", filePath, err)
		}
		if err := mergeDropins(reader, dir, filename, baseUnit); err != nil {
			return nil, fmt.Errorf("merging drop-ins for %q: %w", filename, err)
		}

		mergedContents, err := baseUnit.Write()
		if err != nil {
			return nil, fmt.Errorf("serializing merged quadlet: %w", err)
		}
		spec, err := common.ParseQuadletReferences(mergedContents)
		if err != nil {
			return nil, fmt.Errorf("parsing quadlet spec from %q: %w", filePath, err)
		}

		quadlets[filename] = spec
	}

	if len(quadlets) == 0 {
		return nil, fmt.Errorf("%w in directory: %s", errors.ErrNoQuadletFile, dir)
	}

	return quadlets, nil
}
