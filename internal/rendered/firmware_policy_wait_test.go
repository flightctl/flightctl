package rendered

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestFirmwarePolicyIndependentWaiters(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	subscriber := queues.NewMockPubSubSubscriber(ctrl)
	subscription := queues.NewMockSubscription(ctrl)
	provider.EXPECT().NewPubSubSubscriber(gomock.Any(), FirmwarePolicyChannel).Return(subscriber, nil)
	var handle queues.PubSubHandler
	subscriber.EXPECT().Subscribe(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, h queues.PubSubHandler) (queues.Subscription, error) {
		handle = h
		return subscription, nil
	})
	subscription.EXPECT().Close()
	subscriber.EXPECT().Close()
	hub, err := NewFirmwarePolicyHub(context.Background(), provider)
	require.NoError(t, err)
	defer hub.Close()
	org := uuid.New()
	first, unwatch := hub.Watch(org, "device")
	defer unwatch()
	second, unwatch2 := hub.Watch(org, "device")
	defer unwatch2()
	other, unwatchOther := hub.Watch(uuid.New(), "device")
	defer unwatchOther()
	payload, err := json.Marshal(serializedResourceId{OrgId: org, Name: "device", Type: NotificationTypeFirmwarePolicy, PolicyRevision: 2})
	require.NoError(t, err)
	require.NoError(t, handle(context.Background(), payload, logrus.New()))
	for _, ch := range []<-chan struct{}{first, second} {
		select {
		case <-ch:
		default:
			t.Fatal("matching waiter did not wake")
		}
	}
	select {
	case <-other:
		t.Fatal("cross-tenant waiter woke")
	default:
	}
	unwatch()
	require.NoError(t, handle(context.Background(), payload, logrus.New()))
	select {
	case <-first:
		t.Fatal("removed waiter woke")
	default:
	}
	require.Error(t, handle(context.Background(), []byte("invalid"), logrus.New()))
}

func TestFirmwarePolicyHubConstructorFailures(t *testing.T) {
	_, err := NewFirmwarePolicyHub(context.Background(), nil)
	require.Error(t, err)
	for _, mode := range []string{"publisher error", "nil subscriber", "subscription error", "nil subscription"} {
		t.Run("When "+mode+" it should reject construction", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			provider := queues.NewMockProvider(ctrl)
			subscriber := queues.NewMockPubSubSubscriber(ctrl)
			switch mode {
			case "publisher error":
				provider.EXPECT().NewPubSubSubscriber(gomock.Any(), FirmwarePolicyChannel).Return(nil, context.Canceled)
			case "nil subscriber":
				provider.EXPECT().NewPubSubSubscriber(gomock.Any(), FirmwarePolicyChannel).Return(nil, nil)
			default:
				provider.EXPECT().NewPubSubSubscriber(gomock.Any(), FirmwarePolicyChannel).Return(subscriber, nil)
				subscriber.EXPECT().Close()
				if mode == "subscription error" {
					subscriber.EXPECT().Subscribe(gomock.Any(), gomock.Any()).Return(nil, context.Canceled)
				} else {
					subscriber.EXPECT().Subscribe(gomock.Any(), gomock.Any()).Return(nil, nil)
				}
			}
			_, err := NewFirmwarePolicyHub(context.Background(), provider)
			require.Error(t, err)
		})
	}
}
