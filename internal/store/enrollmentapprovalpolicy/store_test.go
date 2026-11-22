package enrollmentapprovalpolicy_test

import (
	"testing"

	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStoreDependencies(t *testing.T) {
	t.Run("When the database is nil it should panic", func(t *testing.T) { require.Panics(t, func() { policystore.NewStore(nil, logrus.New()) }) })
	t.Run("When the logger is nil it should panic", func(t *testing.T) { require.Panics(t, func() { policystore.NewStore(&gorm.DB{}, nil) }) })
	t.Run("When dependencies are supplied it should construct", func(t *testing.T) { require.NotNil(t, policystore.NewStore(&gorm.DB{}, logrus.New())) })
}
