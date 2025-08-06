package periodic

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestChannelManager_PublishTask_Success(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	cm := NewChannelManager(ChannelManagerConfig{
		Log:               logger,
		ChannelBufferSize: 10,
	})
	defer cm.Close()

	ctx := context.Background()
	taskRef := PeriodicTaskReference{
		Type:  PeriodicTaskTypeResourceSync,
		OrgID: uuid.New(),
	}

	err := cm.PublishTask(ctx, taskRef)
	require.NoError(t, err)
}

func TestChannelManager_PublishTask_ClosedChannel(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	cm := NewChannelManager(ChannelManagerConfig{
		Log:               logger,
		ChannelBufferSize: 10,
	})

	cm.Close()

	ctx := context.Background()
	taskRef := PeriodicTaskReference{
		Type:  PeriodicTaskTypeResourceSync,
		OrgID: uuid.New(),
	}

	err := cm.PublishTask(ctx, taskRef)
	require.Error(t, err)
	require.Contains(t, err.Error(), "channel manager is closed")
}

func TestChannelManager_PublishTask_ContextCancelled(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	cm := NewChannelManager(ChannelManagerConfig{
		Log:               logger,
		ChannelBufferSize: 1,
	})
	defer cm.Close()

	// Fill the channel to make it block
	fillTaskRef := PeriodicTaskReference{
		Type:  PeriodicTaskTypeResourceSync,
		OrgID: uuid.New(),
	}
	err := cm.PublishTask(context.Background(), fillTaskRef)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	taskRef := PeriodicTaskReference{
		Type:  PeriodicTaskTypeResourceSync,
		OrgID: uuid.New(),
	}

	err = cm.PublishTask(ctx, taskRef)
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)
}
