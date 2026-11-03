package worker_client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestDeviceStatusQueueRouting(t *testing.T) {
	for _, tt := range []struct {
		name       string
		kind       domain.ResourceKind
		reason     domain.EventReason
		fields     []domain.ResourceUpdatedDetailsUpdatedFields
		omitFields bool
		withStatus bool
		wantStatus bool
	}{
		{name: "When a device status changes it should use the status queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.UpdatedFieldStatus}, withStatus: true, wantStatus: true},
		{name: "When a device spec changes it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Spec}, withStatus: true},
		{name: "When device labels change it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels}, withStatus: true},
		{name: "When status and spec change it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.UpdatedFieldStatus, domain.Spec}, withStatus: true},
		{name: "When a device is created it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceCreated, omitFields: true, withStatus: true},
		{name: "When another resource status changes it should use the main queue", kind: domain.FleetKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.UpdatedFieldStatus}, withStatus: true},
		{name: "When update details are absent it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, omitFields: true, withStatus: true},
		{name: "When changed fields are empty it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, withStatus: true},
		{name: "When a status publisher is absent it should retain main queue delivery", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.UpdatedFieldStatus}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := domain.Event{Reason: tt.reason, InvolvedObject: domain.ObjectReference{Kind: string(tt.kind), Name: "device"}}
			if !tt.omitFields {
				var details domain.EventDetails
				require.NoError(t, details.FromResourceUpdatedDetails(domain.ResourceUpdatedDetails{DetailType: domain.ResourceUpdated, UpdatedFields: tt.fields}))
				event.Details = &details
			}
			taskProducer, statusProducer := &recordingProducer{}, &recordingProducer{}
			var opts []ClientOption
			if tt.withStatus {
				opts = append(opts, WithStatusPublisher(statusProducer))
			}
			client := NewWorkerClient(taskProducer, logrus.New(), opts...).(ReliableWorkerClient)
			orgID := uuid.New()
			require.NoError(t, client.EmitEventWithError(context.Background(), orgID, &event))
			payloads := taskProducer.payloads
			if tt.wantStatus {
				require.Empty(t, taskProducer.payloads)
				payloads = statusProducer.payloads
			} else {
				require.Empty(t, statusProducer.payloads)
			}
			require.Len(t, payloads, 1)
			var got EventWithOrgId
			require.NoError(t, json.Unmarshal(payloads[0], &got))
			require.Equal(t, orgID, got.OrgId)
			require.Equal(t, event, got.Event)
		})
	}
}

func TestDeviceStatusQueueMalformedDetails(t *testing.T) {
	for _, payload := range []string{
		`{"detailType":"ResourceUpdated","updatedFields":7}`,
		`{"detailType":"Other","updatedFields":["status"]}`,
	} {
		var details domain.EventDetails
		require.NoError(t, json.Unmarshal([]byte(payload), &details))
		event := domain.Event{Reason: domain.EventReasonResourceUpdated, InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind}, Details: &details}
		require.False(t, IsDeviceStatusQueueEvent(event))
	}
}

func TestStatusQueuePublisherAndFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	producer := queues.NewMockQueueProducer(ctrl)
	ctx := context.Background()
	provider.EXPECT().NewQueueProducer(ctx, consts.DeviceStatusTaskQueue).Return(producer, nil)
	publisher, err := StatusQueuePublisher(ctx, provider)
	require.NoError(t, err)
	producer.EXPECT().Enqueue(ctx, gomock.Any(), gomock.Any()).Return(errors.New("status queue unavailable"))
	var details domain.EventDetails
	require.NoError(t, details.FromResourceUpdatedDetails(domain.ResourceUpdatedDetails{DetailType: domain.ResourceUpdated, UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.UpdatedFieldStatus}}))
	event := domain.Event{Reason: domain.EventReasonResourceUpdated, InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind}, Details: &details}
	mainProducer := &recordingProducer{}
	client := NewWorkerClient(mainProducer, logrus.New(), WithStatusPublisher(publisher)).(ReliableWorkerClient)
	require.ErrorContains(t, client.EmitEventWithError(ctx, uuid.New(), &event), "status queue unavailable")
	require.Empty(t, mainProducer.payloads)
}
