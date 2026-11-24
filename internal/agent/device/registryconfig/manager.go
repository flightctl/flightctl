package registryconfig

import (
	"context"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/userutil"
)

// Manager stages and applies native authentication from rendered registry resources.
type Manager interface {
	Prepare(context.Context, *v1beta1.DeviceSpec) error
}

type Account struct {
	UID, GID uint32
	HomeDir  string
}

type Option func(*manager)

func WithAccountLookup(lookup func(v1beta1.Username) (Account, error)) Option {
	return func(m *manager) { m.lookup = lookup }
}

type manager struct {
	factory fileio.ReadWriterFactory
	dataDir string
	lookup  func(v1beta1.Username) (Account, error)
}

func NewManager(factory fileio.ReadWriterFactory, dataDir string, opts ...Option) Manager {
	m := &manager{
		factory: factory,
		dataDir: dataDir,
		lookup: func(name v1beta1.Username) (Account, error) {
			uid, gid, home, err := userutil.LookupExecutionUser(name)
			return Account{UID: uid, GID: gid, HomeDir: home}, err
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *manager) Prepare(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	_, err := m.plan(ctx, desired)
	return err
}
