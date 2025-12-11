package provider

import (
	"context"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

const (
	quadletDropInFile         = "99-flightctl.conf"
	embeddedQuadletMarkerFile = ".flightctl-embedded"
)

type quadletInstaller struct {
	readWriter fileio.ReadWriter
	logger     *log.PrefixLogger
	path       string
	appID      string
}

// installQuadlet prepares Podman quadlet files for use with flightctl by applying namespacing,
// updating cross-references, and adding flightctl-specific overrides.
//
// This function performs three main operations on quadlet files located at the specified path:
//
//  1. NAMESPACING: Renames quadlet files and drop-in directories to include the appID as a prefix.
//     This prevents naming conflicts when multiple applications define similarly-named resources.
//
//     Files are renamed: foo.container → {appID}-foo.container
//     The function is idempotent - already-namespaced files are not renamed again.
//
//     Drop-in directories follow systemd's hierarchical naming rules and are also namespaced:
//     - Specific drop-ins: web.container.d/ → {appID}-web.container.d/
//     - Hierarchical drop-ins: foo-.container.d/ → {appID}-foo-.container.d/
//     - Top-level type drop-ins: container.d/ → {appID}-.container.d/
//
//  2. REFERENCE UPDATING: Updates cross-references within quadlet files and drop-in configurations
//     to point to the newly namespaced resources.
//
//     This includes:
//     - Quadlet-specific references (Volume=, Network=, Image=, Pod=, Mount= parameters)
//     - Systemd references in [Unit] sections (After=, Requires=, Before=, etc.)
//     - Systemd references in [Install] sections (WantedBy=, RequiredBy=, etc.)
//
//     External system services (e.g., chronyd.service, network.target) are not modified.
//     Both quadlet files and drop-in .conf files have their references updated.
//
// 3. FLIGHTCTL OVERRIDES: Creates drop-in configuration files to add flightctl-specific settings.
//
//	For each quadlet type found, creates {appID}-.{type}.d/99-flightctl.conf containing:
//	  - Label with project identifier for filtering (io.flightctl.quadlet.project={appID})
//	  - EnvironmentFile directive pointing to .env (containers only, if .env exists)
//
//	The 99- prefix ensures these overrides have high priority and are not overridden by
//	user-provided drop-ins.
//
// Parameters:
//   - readWriter: File system interface for reading and writing files
//   - path: Absolute path to directory containing quadlet files and drop-in directories
//   - appID: Application identifier used as namespace prefix for all resources
//
// Returns:
//   - error if any operation fails during namespacing, reference updating, or override creation
//
// Example directory structure transformation:
//
//	Before:
//	  /path/
//	    web.container
//	    data.volume
//	    web.container.d/
//	      10-custom.conf
//	    .env
//
//	After:
//	  /path/
//	    myapp-web.container      (namespaced, references updated)
//	    myapp-data.volume        (namespaced)
//	    myapp-web.container.d/
//	      10-custom.conf         (references updated)
//	    myapp-.container.d/
//	      99-flightctl.conf      (flightctl overrides)
//	    myapp-.volume.d/
//	      99-flightctl.conf      (flightctl overrides)
//	    .env                     (preserved as-is)
func installQuadlet(readWriter fileio.ReadWriter, logger *log.PrefixLogger, path string, appID string) error {
	q := &quadletInstaller{
		readWriter: readWriter,
		logger:     logger,
		path:       path,
		appID:      appID,
	}
	return q.install()
}

func (q *quadletInstaller) install() error {
	entries, err := q.readWriter.ReadDir(q.path)
	if err != nil {
		return fmt.Errorf("reading directory: %w", err)
	}

	hasEnvFile := false
	foundTypes := make(map[string]struct{})
	quadletBasenames := make(map[string]struct{})
	var serviceNames []string

	// Collect service and quadlet names and namespace files and drop-in dirs
	for _, entry := range entries {
		if !entry.IsDir() {
			filename := entry.Name()

			if filename == ".env" {
				q.logger.Tracef("Found environment file for quadlet: %s", q.appID)
				hasEnvFile = true
				continue
			}

			ext := filepath.Ext(filename)
			isQuadlet := quadlet.IsQuadletFile(filename)
			if isQuadlet || ext == ".target" {
				basename := strings.TrimSuffix(filename, ext)
				basename = strings.TrimPrefix(basename, fmt.Sprintf("%s-", q.appID))
				quadletBasenames[basename] = struct{}{}
				if isQuadlet {
					foundTypes[ext] = struct{}{}

					defaultSvc, err := defaultServiceName(basename, ext)
					if err != nil {
						return err
					}
					svcName, err := q.getServiceName(filepath.Join(q.path, filename), ext, defaultSvc)
					if err != nil {
						return fmt.Errorf("getting service name for %s: %w", filename, err)
					}
					serviceNames = append(serviceNames, svcName)
					quadletBasenames[strings.TrimSuffix(svcName, ".service")] = struct{}{}
				}

				if err := q.namespaceQuadletFile(filename); err != nil {
					return fmt.Errorf("namespacing %s: %w", filename, err)
				}
			}
		} else {
			if err = q.namespaceDropInDirectory(filepath.Join(q.path, entry.Name())); err != nil {
				return fmt.Errorf("namespacing drop-in dir %s: %w", entry.Name(), err)
			}
		}
	}

	// Create target file with collected service names
	if err := q.createFlightctlTarget(serviceNames); err != nil {
		return fmt.Errorf("creating flightctl target: %w", err)
	}

	entries, err = q.readWriter.ReadDir(q.path)
	if err != nil {
		return fmt.Errorf("re-reading directory: %w", err)
	}

	// Update any required references in quadlet files or in drop-in .conf files
	for _, entry := range entries {
		if !entry.IsDir() {
			filename := entry.Name()
			ext := filepath.Ext(filename)
			if quadlet.IsQuadletFile(filename) || ext == ".target" {
				if err := q.updateQuadletReferences(filename, ext, quadletBasenames); err != nil {
					return fmt.Errorf("updating references in %s: %w", filename, err)
				}
			}
		} else {
			if err = q.updateDropInReferences(filepath.Join(q.path, entry.Name()), quadletBasenames); err != nil {
				return fmt.Errorf("updating drop-in references: %w", err)
			}
		}
	}

	// For any quadlet types that were found, apply flightctl overrides
	for ext := range foundTypes {
		if err := q.createQuadletDropIn(ext, hasEnvFile); err != nil {
			return fmt.Errorf("creating drop-in for %s: %w", ext, err)
		}
	}

	// Copy the target file to the systemd unit directory
	if err := q.copyTargetToSystemd(); err != nil {
		return fmt.Errorf("copying target to systemd: %w", err)
	}

	return nil
}

func namespacedQuadlet(appID string, name string) string {
	if isNamespaced(name, appID) {
		return name
	}
	return quadlet.NamespaceResource(appID, name)
}

func isNamespaced(name, appID string) bool {
	return strings.HasPrefix(name, fmt.Sprintf("%s-", appID))
}

func (q *quadletInstaller) isNamespaced(name string) bool {
	return isNamespaced(name, q.appID)
}

// namespaceQuadletFile renames a quadlet file to include the appID prefix if it doesn't already have it
func (q *quadletInstaller) namespaceQuadletFile(filename string) error {
	if q.isNamespaced(filename) {
		return nil
	}
	q.logger.Tracef("Namespacing quadlet file %s", filename)

	oldPath := filepath.Join(q.path, filename)
	newPath := filepath.Join(q.path, namespacedQuadlet(q.appID, filename))

	if err := q.readWriter.CopyFile(oldPath, newPath); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	if err := q.readWriter.RemoveFile(oldPath); err != nil {
		return fmt.Errorf("removing original file: %w", err)
	}

	return nil
}

// namespaceDropInDirectory renames drop-in directories to match namespaced quadlet files
// For example: web.container.d/ -> myapp-web.container.d/
// Also handles hierarchical drop-ins: foo-bar.container.d/, foo-.container.d/, container.d/ -> myapp-foo-bar.container.d/, myapp-foo-.container.d/, myapp-.container.d/
func (q *quadletInstaller) namespaceDropInDirectory(dirPath string) error {
	dirname := filepath.Base(dirPath)

	// ensure drop-in dir
	if !strings.HasSuffix(dirname, ".d") {
		return nil
	}
	q.logger.Tracef("Namespacing dropin directory: %s for app: %s", dirname, q.appID)

	// Check if it's a quadlet drop-in directory (e.g., web.container.d, container.d, foo-.container.d)
	baseName := strings.TrimSuffix(dirname, ".d")
	ext := filepath.Ext(baseName)

	// handle top level drop-ins like container.d
	topLevelDropIn := false
	if ext == "" {
		topLevelDropIn = true
		ext = fmt.Sprintf(".%s", baseName)
	}

	if _, ok := quadlet.Extensions[ext]; !ok {
		return nil
	}

	if q.isNamespaced(dirname) {
		return nil
	}

	var newDirname string
	if topLevelDropIn {
		newDirname = namespacedQuadlet(q.appID, fmt.Sprintf(".%s", dirname))
	} else {
		newDirname = namespacedQuadlet(q.appID, dirname)
	}

	oldPath := dirPath
	newPath := filepath.Join(filepath.Dir(dirPath), newDirname)

	dropInEntries, err := q.readWriter.ReadDir(oldPath)
	if err != nil {
		return fmt.Errorf("reading drop-in directory %s: %w", oldPath, err)
	}

	if err = q.readWriter.MkdirAll(newPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating new drop-in directory: %w", err)
	}

	for _, dropInEntry := range dropInEntries {
		if dropInEntry.IsDir() {
			continue
		}

		oldFilePath := filepath.Join(oldPath, dropInEntry.Name())
		newFilePath := filepath.Join(newPath, dropInEntry.Name())

		if err = q.readWriter.CopyFile(oldFilePath, newFilePath); err != nil {
			return fmt.Errorf("copying %s: %w", oldFilePath, err)
		}
	}

	if err = q.readWriter.RemoveAll(oldPath); err != nil {
		return fmt.Errorf("removing old drop-in directory: %w", err)
	}

	return nil
}

// createQuadletDropIn creates a drop-in override directory and configuration file
// for a specific quadlet type. It adds the project label, PartOf directive, and optionally the EnvironmentFile parameter.
func (q *quadletInstaller) createQuadletDropIn(extension string, hasEnvFile bool) error {
	q.logger.Tracef("Creating drop-in for %s for app: %s", extension, q.appID)
	dropInDir := filepath.Join(q.path, fmt.Sprintf("%s-%s.d", q.appID, extension))
	if err := q.readWriter.MkdirAll(dropInDir, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating drop-in directory: %w", err)
	}

	sectionName := quadlet.Extensions[extension]

	unit := quadlet.NewEmptyUnit()
	unit.Add("Unit", "PartOf", quadlet.NamespaceResource(q.appID, lifecycle.QuadletTargetName))

	// add label for tracking quadlet events by app id
	switch extension {
	case quadlet.ImageExtension:
		// no labels for Image quadlets
	case quadlet.PodExtension:
		// Pod quadlets don't have first class support for the LabelKey until v5.6
		unit.Add(sectionName, quadlet.PodmanArgsKey, fmt.Sprintf("--label=%s=%s", client.QuadletProjectLabelKey, q.appID))
	default:
		unit.Add(sectionName, quadlet.LabelKey, fmt.Sprintf("%s=%s", client.QuadletProjectLabelKey, q.appID))
	}

	// Only containers support environment files
	if hasEnvFile && extension == quadlet.ContainerExtension {
		unit.Add(sectionName, quadlet.EnvironmentFileKey, filepath.Join(q.path, ".env"))
	}

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing drop-in: %w", err)
	}

	if err := q.readWriter.WriteFile(filepath.Join(dropInDir, quadletDropInFile), contents, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing drop-in file: %w", err)
	}

	return nil
}

func defaultServiceName(basename, ext string) (string, error) {
	switch ext {
	case quadlet.ContainerExtension:
		return fmt.Sprintf("%s.service", basename), nil
	case quadlet.PodExtension:
		return fmt.Sprintf("%s-pod.service", basename), nil
	case quadlet.VolumeExtension:
		return fmt.Sprintf("%s-volume.service", basename), nil
	case quadlet.NetworkExtension:
		return fmt.Sprintf("%s-network.service", basename), nil
	case quadlet.ImageExtension:
		return fmt.Sprintf("%s-image.service", basename), nil
	default:
		return "", fmt.Errorf("%w: %s", common.ErrUnsupportedQuadletType, ext)
	}
}

func (q *quadletInstaller) getServiceName(filePath, ext, defaultName string) (string, error) {
	// currently only Pods (and Artifacts but those are unsupported) allow for specifying a custom service name
	if ext != quadlet.PodExtension {
		return defaultName, nil
	}
	q.logger.Tracef("Parsing service override for: %s for app: %s", filePath, q.appID)

	// Read the contents of the Pod quadlet for a ServiceName override. If one exists, return that. Otherwise, return
	// the default service name
	contents, err := q.readWriter.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	unit, err := quadlet.NewUnit(contents)
	if err != nil {
		return "", fmt.Errorf("parsing quadlet: %w", err)
	}
	name, err := unit.Lookup(quadlet.PodGroup, quadlet.ServiceNameKey)
	if err != nil {
		if errors.Is(err, quadlet.ErrKeyNotFound) {
			q.logger.Debugf("Quadlet service name not found for quadlet: %s app: %s. Defaulting to name: %s", q.appID, filePath, defaultName)
			return defaultName, nil
		}
		return "", err
	}
	if filepath.Ext(name) == ".service" {
		return name, nil
	}
	return fmt.Sprintf("%s.service", name), nil
}

func (q *quadletInstaller) createFlightctlTarget(serviceNames []string) error {
	q.logger.Tracef("Creating flightctl target for app: %s", q.appID)
	unit := quadlet.NewEmptyUnit()
	for _, svc := range serviceNames {
		unit.Add("Unit", "Wants", svc)
		unit.Add("Unit", "After", svc)
	}

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing target: %w", err)
	}

	targetPath := filepath.Join(q.path, quadlet.NamespaceResource(q.appID, lifecycle.QuadletTargetName))
	return q.readWriter.WriteFile(targetPath, contents, fileio.DefaultFilePermissions)
}

func (q *quadletInstaller) copyTargetToSystemd() error {
	q.logger.Tracef("Copying flightctl target to systemd path for app: %s", q.appID)
	targetName := quadlet.NamespaceResource(q.appID, lifecycle.QuadletTargetName)
	srcPath := filepath.Join(q.path, targetName)
	dstPath := filepath.Join(lifecycle.QuadletTargetPath, targetName)
	if err := q.readWriter.MkdirAll(lifecycle.QuadletTargetPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating target directory: %w", err)
	}

	return q.readWriter.CopyFile(srcPath, dstPath)
}

// prefixQuadletReference prefixes a quadlet filename reference with appID if it's not already prefixed
func prefixQuadletReference(value, appID string) string {
	for ext := range quadlet.Extensions {
		if strings.HasSuffix(value, ext) {
			prefix := fmt.Sprintf("%s-", appID)
			if !strings.HasPrefix(value, prefix) {
				return namespacedQuadlet(appID, value)
			}
			return value
		}
	}
	return value
}

// namespaceVolumeName namespaces volume names while preserving host paths and anonymous volumes
func namespaceVolumeName(value, appID string) string {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 0 {
		return value
	}

	volumePart := parts[0]
	if strings.HasPrefix(volumePart, "/") {
		return value
	}

	prefix := fmt.Sprintf("%s-", appID)
	if !strings.HasPrefix(volumePart, prefix) {
		parts[0] = namespacedQuadlet(appID, volumePart)
	}

	return strings.Join(parts, ":")
}

// namespaceNetworkName namespaces custom network names while preserving built-in network modes
func namespaceNetworkName(value, appID string) string {
	// see https://docs.podman.io/en/latest/markdown/podman-run.1.html#network-mode-net
	builtInModes := map[string]struct{}{"bridge": {}, "host": {}, "none": {}, "private": {}, "slirp4netns": {}, "pasta": {}}
	parts := strings.Split(value, ":")
	baseName := parts[0]
	if _, ok := builtInModes[baseName]; ok {
		return value
	}

	if baseName == "container" && len(parts) > 1 {
		parts[1] = namespacedQuadlet(appID, parts[1])
		return strings.Join(parts, ":")
	}

	prefixed := prefixQuadletReference(value, appID)
	if prefixed != value {
		return prefixed
	}

	prefix := fmt.Sprintf("%s-", appID)
	if !strings.HasPrefix(value, prefix) {
		return namespacedQuadlet(appID, value)
	}

	return value
}

// updateSystemdReference updates references in [Unit] and [Install] sections
// It handles both direct quadlet references and service references generated by our quadlets
func (q *quadletInstaller) updateSystemdReference(value string, quadletBasenames map[string]struct{}) string {
	ext := filepath.Ext(value)
	if ext == ".service" || ext == ".target" {
		basename := strings.TrimSuffix(value, ext)
		if _, exists := quadletBasenames[basename]; exists {
			return namespacedQuadlet(q.appID, value)
		}
		return value
	}

	return prefixQuadletReference(value, q.appID)
}

// updateSpaceSeparatedReferences updates space-separated systemd references
func (q *quadletInstaller) updateSpaceSeparatedReferences(value string, quadletBasenames map[string]struct{}) string {
	parts := strings.Fields(value)
	for i, part := range parts {
		parts[i] = q.updateSystemdReference(part, quadletBasenames)
	}
	return strings.Join(parts, " ")
}

// updateMountValue updates Mount= parameter values to prefix quadlet references
func updateMountValue(value, appID string) (string, error) {
	mountType, err := quadlet.MountType(value)
	if err != nil {
		return "", fmt.Errorf("parsing mount type %q: %w", value, err)
	}
	if !slices.Contains([]string{"volume", "image"}, mountType) {
		return value, nil
	}

	mountParts, err := quadlet.MountParts(value)
	if err != nil {
		return "", fmt.Errorf("parsing mount parts %q: %w", value, err)
	}

	for i, part := range mountParts {
		kv := strings.Split(part, "=")
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		if key == "source" || key == "src" {
			var namespacedValue string
			if mountType == "volume" {
				namespacedValue = namespaceVolumeName(val, appID)
			} else {
				namespacedValue = prefixQuadletReference(val, appID)
			}
			mountParts[i] = fmt.Sprintf("%s=%s", key, namespacedValue)
		}
	}

	var buf strings.Builder
	writer := csv.NewWriter(&buf)
	if err := writer.Write(mountParts); err != nil {
		return "", fmt.Errorf("writing mount value: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("writing mount value: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// updateVolumeValue updates Volume= parameter values to namespace volume names
func updateVolumeValue(value, appID string) (string, error) {
	return namespaceVolumeName(value, appID), nil
}

// updateSystemdSection updates references in [Unit] or [Install] sections
func (q *quadletInstaller) updateSystemdSection(unit *quadlet.Unit, section string, quadletBasenames map[string]struct{}) error {
	if unit.HasSection(section) {
		return unit.TransformAll(section, func(_, value string) (string, error) {
			return q.updateSpaceSeparatedReferences(value, quadletBasenames), nil
		})
	}
	return nil
}

var quadletNamespaceRules = map[string][]struct {
	key       string
	transform func(string) quadlet.UnitEntryTransformFn
}{
	quadlet.ContainerGroup: {
		{
			key: quadlet.ImageKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return prefixQuadletReference(val, appID), nil }
			},
		},
		{
			key: quadlet.PodKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return prefixQuadletReference(val, appID), nil }
			},
		},
		{
			key: quadlet.NetworkKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespaceNetworkName(val, appID), nil }
			},
		},
		{
			key: quadlet.MountKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return updateMountValue(val, appID) }
			},
		},
		{
			key: quadlet.VolumeKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return updateVolumeValue(val, appID) }
			},
		},
		{
			key: quadlet.ContainerNameKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespacedQuadlet(appID, val), nil }
			},
		},
	},
	quadlet.PodGroup: {
		{
			key: quadlet.NetworkKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespaceNetworkName(val, appID), nil }
			},
		},
		{
			key: quadlet.VolumeKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return updateVolumeValue(val, appID) }
			},
		},
		{
			key: quadlet.PodNameKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespacedQuadlet(appID, val), nil }
			},
		},
		{
			key: quadlet.ServiceNameKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespacedQuadlet(appID, val), nil }
			},
		},
	},
	quadlet.VolumeGroup: {
		{
			key: quadlet.ImageKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return prefixQuadletReference(val, appID), nil }
			},
		},
		{
			key: quadlet.VolumeNameKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespacedQuadlet(appID, val), nil }
			},
		},
	},
	quadlet.NetworkGroup: {
		{
			key: quadlet.NetworkNameKey,
			transform: func(appID string) quadlet.UnitEntryTransformFn {
				return func(val string) (string, error) { return namespacedQuadlet(appID, val), nil }
			},
		},
	},
}

// updateQuadletReferences updates cross-references within a quadlet file after it has been namespaced
func (q *quadletInstaller) updateQuadletReferences(filename, extension string, quadletBasenames map[string]struct{}) error {
	return q.updateQuadletReferencesInDir(q.path, filename, extension, quadletBasenames)
}

func (q *quadletInstaller) updateQuadletReferencesInDir(dirPath, filename, extension string, quadletBasenames map[string]struct{}) error {
	filePath := filepath.Join(dirPath, filename)
	content, err := q.readWriter.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	unit, err := quadlet.NewUnit(content)
	if err != nil {
		return fmt.Errorf("deserializing quadlet: %w", err)
	}

	if err := q.updateSystemdSection(unit, "Unit", quadletBasenames); err != nil {
		return fmt.Errorf("updating systemd Unit section: %w", err)
	}
	if err := q.updateSystemdSection(unit, "Install", quadletBasenames); err != nil {
		return fmt.Errorf("updating systemd Install section: %w", err)
	}

	// if updating the references within a quadlet file, apply the specified rules
	if section, ok := quadlet.Extensions[extension]; ok {
		var transformErrs []error
		for _, rule := range quadletNamespaceRules[section] {
			if !unit.HasSection(section) {
				continue
			}
			if err := unit.Transform(section, rule.key, rule.transform(q.appID)); err != nil {
				transformErrs = append(transformErrs, err)
			}
		}
		if len(transformErrs) > 0 {
			return fmt.Errorf("applying quadlet namespacing rules: %w", errors.Join(transformErrs...))
		}
	}

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing sections: %w", err)
	}

	if err := q.readWriter.WriteFile(filePath, contents, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}

// updateDropInReferences updates references within drop-in .conf files after drop-in directories have been namespaced
func (q *quadletInstaller) updateDropInReferences(dirPath string, quadletBasenames map[string]struct{}) error {
	dirname := filepath.Base(dirPath)

	if !strings.HasSuffix(dirname, ".d") {
		return nil
	}

	if !q.isNamespaced(dirname) {
		return nil
	}

	baseName := strings.TrimSuffix(dirname, ".d")
	ext := filepath.Ext(baseName)

	if _, ok := quadlet.Extensions[ext]; !ok {
		return nil
	}

	dropInPath := dirPath
	confEntries, err := q.readWriter.ReadDir(dropInPath)
	if err != nil {
		return fmt.Errorf("reading drop-in directory %s: %w", dropInPath, err)
	}

	for _, confEntry := range confEntries {
		if confEntry.IsDir() {
			continue
		}

		confFilename := confEntry.Name()
		if !strings.HasSuffix(confFilename, ".conf") {
			continue
		}

		if err = q.updateQuadletReferencesInDir(dropInPath, confFilename, ext, quadletBasenames); err != nil {
			return fmt.Errorf("updating drop-in: %w", err)
		}
	}

	return nil
}

func createVolumeQuadlet(rw fileio.ReadWriter, dir string, volumeName string, imageRef string) error {
	unit := quadlet.NewEmptyUnit()
	unit.Add(quadlet.VolumeGroup, quadlet.ImageKey, imageRef)
	unit.Add(quadlet.VolumeGroup, quadlet.DriverKey, "image")

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing volume quadlet: %w", err)
	}

	volumeFile := filepath.Join(dir, fmt.Sprintf("%s.volume", volumeName))
	if err := rw.WriteFile(volumeFile, contents, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing volume file: %w", err)
	}

	return nil
}

func generateQuadlet(ctx context.Context, podman *client.Podman, rw fileio.ReadWriter, dir string, spec *v1beta1.ImageApplicationProviderSpec) error {
	unit := quadlet.NewEmptyUnit()
	unit.Add(quadlet.ContainerGroup, quadlet.ImageKey, spec.Image)

	if spec.Resources != nil && spec.Resources.Limits != nil {
		lims := spec.Resources.Limits
		if lims.Cpu != nil {
			unit.Add(quadlet.ContainerGroup, quadlet.PodmanArgsKey, fmt.Sprintf("--cpus %s", *lims.Cpu))
		}
		if lims.Memory != nil {
			// the memory key was made a first class citizen in 5.6
			unit.Add(quadlet.ContainerGroup, quadlet.PodmanArgsKey, fmt.Sprintf("--memory %s", *lims.Memory))
		}
	}
	for _, port := range lo.FromPtr(spec.Ports) {
		unit.Add(quadlet.ContainerGroup, quadlet.PublishPortKey, port)
	}

	// add default values to [Service] and [Install] sections
	unit.Add("Service", "Restart", "on-failure").
		Add("Service", "RestartSec", "60").
		Add("Install", "WantedBy", "multi-user.target default.target")

	for _, vol := range lo.FromPtr(spec.Volumes) {
		volType, err := vol.Type()
		if err != nil {
			return fmt.Errorf("getting volume type: %w", err)
		}

		switch volType {
		case v1beta1.MountApplicationVolumeProviderType:
			mountSpec, err := vol.AsMountVolumeProviderSpec()
			if err != nil {
				return fmt.Errorf("getting mount volume spec: %w", err)
			}
			unit.Add(quadlet.ContainerGroup, quadlet.VolumeKey, fmt.Sprintf("%s:%s", vol.Name, mountSpec.Mount.Path))
		case v1beta1.ImageMountApplicationVolumeProviderType:
			imageMountSpec, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				return fmt.Errorf("getting image mount volume spec: %w", err)
			}

			// if it was previously discovered as an image then we can just populate the volume with the image
			if podman.ImageExists(ctx, imageMountSpec.Image.Reference) {
				if err := createVolumeQuadlet(rw, dir, vol.Name, imageMountSpec.Image.Reference); err != nil {
					return fmt.Errorf("creating volume quadlet for %s: %w", vol.Name, err)
				}
				unit.Add(quadlet.ContainerGroup, quadlet.VolumeKey, fmt.Sprintf("%s.volume:%s", vol.Name, imageMountSpec.Mount.Path))
			} else {
				// if it's an artifact we have to handle it more similarly to compose (named volume that we extract into)
				unit.Add(quadlet.ContainerGroup, quadlet.VolumeKey, fmt.Sprintf("%s:%s", vol.Name, imageMountSpec.Mount.Path))
			}
		default:
			return fmt.Errorf("%w: %s", errors.ErrUnsupportedVolumeType, volType)
		}
	}

	contents, err := unit.Write()
	if err != nil {
		return fmt.Errorf("serializing quadlet: %w", err)
	}

	// namespacing should occur after the quadlet has been generated so it is fine to default to a basic container name
	if err := rw.WriteFile(filepath.Join(dir, "app.container"), contents, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing container quadlet: %w", err)
	}
	return nil
}
