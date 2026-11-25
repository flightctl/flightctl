package enrollmentapprovalpolicy_test

import (
	"testing"

	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPolicyStoreDependencies(t *testing.T) {
	t.Run("When the database is nil it should explain the missing database", func(t *testing.T) {
		store, err := policystore.NewStore(nil, logrus.New())
		require.ErrorContains(t, err, "database is required")
		require.Nil(t, store)
	})
	t.Run("When the logger is a nil pointer it should explain the missing logger", func(t *testing.T) {
		store, err := policystore.NewStore(&gorm.DB{}, (*logrus.Logger)(nil))
		require.ErrorContains(t, err, "logger is required")
		require.Nil(t, store)
	})
	t.Run("When dependencies are supplied it should construct", func(t *testing.T) {
		store, err := policystore.NewStore(&gorm.DB{}, logrus.New())
		require.NoError(t, err)
		require.NotNil(t, store)
	})
}
