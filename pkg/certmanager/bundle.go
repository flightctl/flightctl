package certmanager

import (
	"fmt"
	"strings"
)

// bundle is a simple helper implementation of BundleProvider
type bundle struct {
	name           string
	disableRenewal bool

	configs      map[string]ConfigProvider
	provisioners map[string]ProvisionerFactory
	storages     map[string]StorageFactory
}

var _ BundleProvider = (*bundle)(nil)

type BundleOption func(*bundle) error

// NewBundle constructs a bundle with the given name and options.
func NewBundle(name string, opts ...BundleOption) (BundleProvider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("bundle name is empty")
	}

	b := &bundle{
		name:         name,
		configs:      make(map[string]ConfigProvider),
		provisioners: make(map[string]ProvisionerFactory),
		storages:     make(map[string]StorageFactory),
	}

	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("nil bundle option")
		}
		if err := opt(b); err != nil {
			return nil, err
		}
	}

	return b, nil
}

// WithRenewalDisabled disables time-based renewal for all certificates in the bundle.
// Initial provisioning and reconciliation on config changes still occur.
func WithRenewalDisabled() BundleOption {
	return func(b *bundle) error {
		b.disableRenewal = true
		return nil
	}
}

// WithConfigProvider registers a ConfigProvider in the bundle.
func WithConfigProvider(cp ConfigProvider) BundleOption {
	return func(b *bundle) error {
		if cp == nil {
			return fmt.Errorf("config provider is nil")
		}
		key := strings.TrimSpace(cp.Name())
		if key == "" {
			return fmt.Errorf("config provider has empty Name()")
		}
		if _, exists := b.configs[key]; exists {
			return fmt.Errorf("config provider %q already registered in bundle %q", key, b.name)
		}
		b.configs[key] = cp
		return nil
	}
}

// WithProvisionerFactory registers a provisioner factory.
func WithProvisionerFactory(pf ProvisionerFactory) BundleOption {
	return func(b *bundle) error {
		if pf == nil {
			return fmt.Errorf("provisioner factory is nil")
		}
		key := strings.TrimSpace(pf.Type())
		if key == "" {
			return fmt.Errorf("provisioner factory has empty Type()")
		}
		if _, exists := b.provisioners[key]; exists {
			return fmt.Errorf("provisioner factory %q already registered in bundle %q", key, b.name)
		}
		b.provisioners[key] = pf
		return nil
	}
}

// WithStorageFactory registers a storage factory.
func WithStorageFactory(sf StorageFactory) BundleOption {
	return func(b *bundle) error {
		if sf == nil {
			return fmt.Errorf("storage factory is nil")
		}
		key := strings.TrimSpace(sf.Type())
		if key == "" {
			return fmt.Errorf("storage factory has empty Type()")
		}
		if _, exists := b.storages[key]; exists {
			return fmt.Errorf("storage factory %q already registered in bundle %q", key, b.name)
		}
		b.storages[key] = sf
		return nil
	}
}

// ---- BundleProvider implementation ----

func (b *bundle) Name() string {
	return b.name
}

func (b *bundle) DisableRenewal() bool {
	return b.disableRenewal
}

func (b *bundle) Configs() map[string]ConfigProvider {
	return b.configs
}

func (b *bundle) Provisioners() map[string]ProvisionerFactory {
	return b.provisioners
}

func (b *bundle) Storages() map[string]StorageFactory {
	return b.storages
}
