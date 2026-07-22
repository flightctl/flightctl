package store

import (
	"testing"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
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
