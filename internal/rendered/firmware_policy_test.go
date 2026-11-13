package rendered

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestFirmwarePolicyNotification(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	name := "device"
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	publisher := queues.NewMockPubSubPublisher(ctrl)
	provider.EXPECT().NewPubSubPublisher(gomock.Any(), "firmware_policy_notifier").Return(publisher, nil)
	notifier, err := NewFirmwarePolicyNotifier(provider)
	require.NoError(t, err)
	first := publisher.EXPECT().Publish(gomock.Any(), gomock.Any()).Return(errors.New("broker unavailable"))
	expectedRevisions := []int64{2, 1, 9223372036854775807}
	publication := 0
	publisher.EXPECT().Publish(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, payload []byte) error {
		var message serializedResourceId
		require.NoError(t, json.Unmarshal(payload, &message))
		require.Equal(t, org, message.OrgId)
		require.Equal(t, name, message.Name)
		require.Equal(t, expectedRevisions[publication], message.PolicyRevision)
		publication++
		require.Equal(t, NotificationTypeFirmwarePolicy, message.Type)
		return nil
	}).After(first).Times(3)
	require.Error(t, notifier.Notify(ctx, org, name, 2))
	require.NoError(t, notifier.Notify(ctx, org, name, 2))
	require.NoError(t, notifier.Notify(ctx, org, name, 1))
	require.NoError(t, notifier.Notify(ctx, org, name, 9223372036854775807))
}

func TestFirmwarePolicyNotifierValidation(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	_, err := NewFirmwarePolicyNotifier(nil)
	require.Error(t, err)
	notifier, err := NewFirmwarePolicyNotifier(provider)
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		revision int64
	}{{"", 1}, {"device", 0}, {"device", -1}} {
		require.Error(t, notifier.Notify(context.Background(), uuid.New(), tc.name, tc.revision))
	}
}

func TestFirmwarePolicyPublisherCreationRetry(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	publisher := queues.NewMockPubSubPublisher(ctrl)
	first := provider.EXPECT().NewPubSubPublisher(gomock.Any(), "firmware_policy_notifier").Return(nil, errors.New("unavailable"))
	provider.EXPECT().NewPubSubPublisher(gomock.Any(), "firmware_policy_notifier").Return(publisher, nil).After(first)
	publisher.EXPECT().Publish(gomock.Any(), gomock.Any()).Return(nil)
	notifier, err := NewFirmwarePolicyNotifier(provider)
	require.NoError(t, err)
	require.Error(t, notifier.Notify(context.Background(), uuid.Nil, "device", 1))
	require.NoError(t, notifier.Notify(context.Background(), uuid.Nil, "device", 1))
}
