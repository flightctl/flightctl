package registryconfig

import (
	"context"
	"sync"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/userutil"
)

// Manager stages and applies native authentication from rendered registry resources.
type Manager interface {
	BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error
	Sync(ctx context.Context, desired *v1beta1.DeviceSpec) error
	// AfterUpdate releases staging for the spec instance passed to BeforeUpdate.
	AfterUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error
	StagedPath(livePath string) (string, bool)
	Cleanup() error
}

type Option func(*manager)

func WithAccountLookup(lookup func(v1beta1.Username) (userutil.Account, error)) Option {
	return func(m *manager) { m.lookup = lookup }
}

type manager struct {
	factory fileio.ReadWriterFactory
	dataDir string
	lookup  func(v1beta1.Username) (userutil.Account, error)

	mu          sync.Mutex
	preparedFor *v1beta1.DeviceSpec
	staged      map[string]*stagedOutput
	// Retain previous paths while in-flight pulls can still use them.
	all []*stagedOutput
}

func NewManager(factory fileio.ReadWriterFactory, dataDir string, opts ...Option) Manager {
	m := &manager{
		factory: factory,
		dataDir: dataDir,
		lookup:  userutil.LookupExecutionUser,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}
