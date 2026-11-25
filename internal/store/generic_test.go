package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testResourceModel is a minimal model.ResourceInterface implementer that is
// deliberately not one of the resource model types GenericStore was originally
// written for. Its only purpose is to prove, at compile time and via
// construction, that GenericStore accepts any type whose pointer implements
// model.ResourceInterface rather than a pre-enumerated list of named types.
type testResourceModel struct {
	model.Resource
}

func (m *testResourceModel) GetKind() string {
	return "TestResource"
}

func (m *testResourceModel) HasNilSpec() bool {
	return false
}

func (m *testResourceModel) HasSameSpecAs(_ any) bool {
	return true
}

func (m *testResourceModel) GetStatusAsJson() ([]byte, error) {
	return []byte("{}"), nil
}

type testAPIResource struct{}

type testAPIResourceList struct{}

func TestNewGenericStore_AcceptsResourceModelOutsideOriginalTypeSet(t *testing.T) {
	req := require.New(t)

	genericStore := NewGenericStore[*testResourceModel, testResourceModel, testAPIResource, testAPIResourceList](
		nil,
		logrus.New(),
		func(*testAPIResource) (*testResourceModel, error) {
			return &testResourceModel{}, nil
		},
		func(*testResourceModel, ...model.APIResourceOption) (*testAPIResource, error) {
			return &testAPIResource{}, nil
		},
		func([]testResourceModel, *string, *int64) (testAPIResourceList, error) {
			return testAPIResourceList{}, nil
		},
	)

	req.NotNil(genericStore)
}

func TestGenericStoreMutateRetryBoundary(t *testing.T) {
	deadlock := &pgconn.PgError{Code: "40P01"}
	serializationFailure := &pgconn.PgError{Code: "40001"}
	for _, tc := range []struct {
		name        string
		applyErr    error
		writeErr    error
		retry       bool
		callerOwned bool
		attempts    int
	}{
		{name: "When a deadlock recurs it should cap attempts", writeErr: deadlock, attempts: 3},
		{name: "When serialization keeps failing it should cap attempts", writeErr: serializationFailure, attempts: 3},
		{name: "When an internal version race recurs it should cap attempts", writeErr: flterrors.ErrResourceVersionConflict, retry: true, attempts: 3},
		{name: "When a caller-owned write aborts it should return the original error", writeErr: deadlock, callerOwned: true, attempts: 1},
		{name: "When a caller-owned write races it should leave retry to the owner", writeErr: flterrors.ErrResourceVersionConflict, retry: true, callerOwned: true, attempts: 1},
		{name: "When the write outcome is unknown it should not replay", writeErr: &pgconn.PgError{Code: "08006"}, attempts: 1},
		{name: "When a business error wraps a database cause it should not replay", applyErr: fmt.Errorf("validation failed: %w", serializationFailure), attempts: 1},
		{name: "When the submitted version is stale it should not replay", applyErr: flterrors.ErrResourceVersionConflict, attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.callerOwned {
				ctx = context.WithValue(ctx, txContextKey{}, &gorm.DB{})
			}
			generic := NewGenericStore[*testResourceModel, testResourceModel, testAPIResource, testAPIResourceList](nil, logrus.New(), nil, nil, nil)
			var loads, writes int
			hooks := MutateHooks[testAPIResource]{
				MaxRetries:  lo.ToPtr(2),
				RetryAborts: true,
				Load: func(context.Context, uuid.UUID, string) (*testAPIResource, error) {
					loads++
					return &testAPIResource{}, nil
				},
				Wrap: func(resource *testAPIResource) ResourceMutation[testAPIResource] {
					return &Mutation[testAPIResource]{resource: resource}
				},
				PersistUpdate: func(context.Context, uuid.UUID, string, *testAPIResource, ResourceMutation[testAPIResource]) (bool, error) {
					writes++
					return tc.retry, tc.writeErr
				},
			}
			_, _, _, err := generic.Mutate(ctx, uuid.Nil, "test", nil, hooks, func(ResourceMutation[testAPIResource]) error {
				return tc.applyErr
			})
			require.Equal(t, tc.attempts, loads)
			if tc.applyErr != nil {
				require.Equal(t, tc.applyErr, err)
				require.Zero(t, writes)
			} else {
				require.Same(t, tc.writeErr, err)
				require.Equal(t, tc.attempts, writes)
			}
		})
	}
}
