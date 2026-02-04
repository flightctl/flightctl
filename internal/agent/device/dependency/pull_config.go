package dependency

import (
	"bytes"
	"sync"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

// PullConfigSpec defines a config requirement with fallback paths.
type PullConfigSpec struct {
	// Paths in fallback order - first found/resolved wins.
	Paths []string
	// OptionFn creates a client option from the resolved path.
	OptionFn func(string) client.ClientOption
}

// PullConfigResolver manages the lifecycle of pull configurations
// with centralized cleanup after update completion.
type PullConfigResolver interface {
	// BeforeUpdate initializes the resolver with the desired device spec.
	// Must be called before any Options calls.
	BeforeUpdate(desired *v1beta1.DeviceSpec)

	// Options returns a lazy function that resolves configs when called.
	// For each PullConfigSpec, tries paths in order until one is found.
	// Results are cached; subsequent calls with same paths return cached values.
	Options(specs ...PullConfigSpec) ClientOptsFn

	// Cleanup cleans up all resolved configurations (temp files).
	Cleanup()
}

type resolvedConfig struct {
	path          string
	cleanup       func()
	inlineContent []byte
}

type pullConfigResolver struct {
	log       *log.PrefixLogger
	rwFactory fileio.ReadWriterFactory

	mu       sync.Mutex
	desired  *v1beta1.DeviceSpec
	resolved map[string]*resolvedConfig
}

// NewPullConfigResolver creates a new PullConfigResolver.
func NewPullConfigResolver(log *log.PrefixLogger, rwFactory fileio.ReadWriterFactory) PullConfigResolver {
	return &pullConfigResolver{
		log:       log,
		rwFactory: rwFactory,
	}
}

func (r *pullConfigResolver) BeforeUpdate(desired *v1beta1.DeviceSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()

	oldDesired := r.desired
	r.desired = desired

	if oldDesired == nil {
		r.resolved = make(map[string]*resolvedConfig)
		return
	}

	if r.resolved == nil {
		r.resolved = make(map[string]*resolvedConfig)
		return
	}

	for path, cached := range r.resolved {
		newContent, found := r.authFromSpec(desired, path)

		// exists only on disk
		if !found && cached.inlineContent == nil {
			continue
		}

		// exists inline in the spec
		if found && bytes.Equal(newContent, cached.inlineContent) {
			continue
		}

		if cached.cleanup != nil {
			cached.cleanup()
		}
		delete(r.resolved, path)
	}
}

func (r *pullConfigResolver) Options(specs ...PullConfigSpec) ClientOptsFn {
	return func() []client.ClientOption {
		r.mu.Lock()
		defer r.mu.Unlock()

		if r.desired == nil {
			return nil
		}

		var opts []client.ClientOption
		for _, spec := range specs {
			for _, path := range spec.Paths {
				if cached, ok := r.resolved[path]; ok {
					r.log.Debugf("Using cached inline config from device spec for %s", path)
					opts = append(opts, spec.OptionFn(cached.path))
					break
				}

				resolved, found := r.resolve(path)
				if !found {
					continue
				}

				r.resolved[path] = resolved
				opts = append(opts, spec.OptionFn(resolved.path))
				break
			}
		}
		return opts
	}
}

func (r *pullConfigResolver) resolve(configPath string) (*resolvedConfig, bool) {
	rootRW, err := r.rwFactory(v1beta1.CurrentProcessUsername)
	if err != nil {
		r.log.Warnf("Failed to create read/writer: %v", err)
		return nil, false
	}

	specContent, found := r.authFromSpec(r.desired, configPath)
	if found {
		exists, err := rootRW.PathExists(configPath)
		if err != nil {
			r.log.Warnf("Failed to check path exists: %v", err)
			return nil, false
		}
		if exists {
			diskContent, err := rootRW.ReadFile(configPath)
			if err != nil {
				r.log.Warnf("Failed to read existing config file: %v", err)
				return nil, false
			}

			if bytes.Equal(diskContent, specContent) {
				r.log.Debugf("Using on-disk config (identical to spec): %s", configPath)
				return &resolvedConfig{
					path:          configPath,
					cleanup:       nil,
					inlineContent: specContent,
				}, true
			}
		}

		path, cleanup, err := fileio.WriteTmpFile(rootRW, "config_", "config", specContent, 0600)
		if err != nil {
			r.log.Warnf("Failed to write temp config file: %v", err)
			return nil, false
		}
		r.log.Debugf("Using inline config from device spec for %s", configPath)
		return &resolvedConfig{
			path:          path,
			cleanup:       cleanup,
			inlineContent: specContent,
		}, true
	}

	exists, err := rootRW.PathExists(configPath)
	if err != nil {
		r.log.Warnf("Failed to check path exists: %v", err)
		return nil, false
	}
	if exists {
		r.log.Debugf("Using on-disk config: %s", configPath)
		return &resolvedConfig{
			path:          configPath,
			cleanup:       nil,
			inlineContent: nil,
		}, true
	}

	return nil, false
}

func (r *pullConfigResolver) authFromSpec(device *v1beta1.DeviceSpec, authPath string) ([]byte, bool) {
	if device == nil || device.Config == nil {
		return nil, false
	}

	for _, provider := range *device.Config {
		pType, err := provider.Type()
		if err != nil {
			continue
		}
		if pType != v1beta1.InlineConfigProviderType {
			continue
		}
		spec, err := provider.AsInlineConfigProviderSpec()
		if err != nil {
			continue
		}
		for _, file := range spec.Inline {
			if file.Path == authPath {
				contents, err := fileio.DecodeContent(file.Content, file.ContentEncoding)
				if err != nil {
					r.log.Warnf("Failed to decode content for %s: %v", authPath, err)
					continue
				}
				return contents, true
			}
		}
	}

	return nil, false
}

func (r *pullConfigResolver) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, cached := range r.resolved {
		if cached.cleanup != nil {
			cached.cleanup()
		}
	}
	r.resolved = nil
	r.desired = nil
}
