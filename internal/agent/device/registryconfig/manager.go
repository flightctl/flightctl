package registryconfig

import (
	"context"
	"errors"
	"sync"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/userutil"
)

// Manager stages and applies native authentication from rendered registry resources.
type Manager interface {
	BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error
	Sync(ctx context.Context, desired *v1beta1.DeviceSpec) error
	AfterUpdate(ctx context.Context) error
	StagedPath(livePath string) (string, bool)
	Cleanup() error
}

type rollbackCauseKey struct{}

// WithRollbackCause records the error that triggered rollback reconciliation.
func WithRollbackCause(ctx context.Context, cause error) context.Context {
	return context.WithValue(ctx, rollbackCauseKey{}, cause)
}

func rollbackBeforeSync(ctx context.Context) bool {
	cause, _ := ctx.Value(rollbackCauseKey{}).(error)
	return cause != nil && !errors.Is(cause, deviceerrors.ErrPhaseApplyingUpdate) && !errors.Is(cause, deviceerrors.ErrPhaseActivatingConfig)
}

type Option func(*manager)

func WithAccountLookup(lookup func(v1beta1.Username) (userutil.Account, error)) Option {
	return func(m *manager) { m.lookup = lookup }
}

type manager struct {
	factory fileio.ReadWriterFactory
	dataDir string
	lookup  func(v1beta1.Username) (userutil.Account, error)

	mu     sync.RWMutex
	staged map[string]*stagedOutput
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
