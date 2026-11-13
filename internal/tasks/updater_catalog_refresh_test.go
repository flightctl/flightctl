package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/rendered"
	catalogservice "github.com/flightctl/flightctl/internal/service/updatercatalog"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

func TestUpdaterCatalogRefreshLifecycle(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	notifier, err := rendered.NewFirmwarePolicyNotifier(provider)
	require.NoError(t, err)
	st, err := catalogstore.NewStore(&gorm.DB{})
	require.NoError(t, err)
	service, err := catalogservice.NewService(st)
	require.NoError(t, err)
	_, err = NewUpdaterCatalogRefresh(nil, notifier, logrus.New())
	require.Error(t, err)
	_, err = NewUpdaterCatalogRefresh(service, nil, logrus.New())
	require.Error(t, err)
	_, err = NewUpdaterCatalogRefresh(service, notifier, nil)
	require.Error(t, err)
	task, err := NewUpdaterCatalogRefresh(service, notifier, logrus.New())
	require.NoError(t, err)
	for _, limit := range []int{-1, 0, 257} {
		_, err := task.DispatchPending(context.Background(), limit)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { task.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled worker did not stop")
	}
}
