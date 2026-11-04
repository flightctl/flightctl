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

func TestDeviceLabelSyncQueueRouting(t *testing.T) {
	for _, tt := range []struct {
		name          string
		kind          domain.ResourceKind
		reason        domain.EventReason
		fields        []domain.ResourceUpdatedDetailsUpdatedFields
		withLabelSync bool
		wantLabelSync bool
	}{
		{name: "When device label reconciliation is requested it should use the label sync queue", kind: domain.DeviceKind, reason: domain.EventReasonDeviceLabelReconciliationRequested, withLabelSync: true, wantLabelSync: true},
		{name: "When a device spec changes it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Spec}, withLabelSync: true},
		{name: "When device labels change it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, fields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels}, withLabelSync: true},
		{name: "When a device is created it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceCreated, withLabelSync: true},
		{name: "When another resource requests device label reconciliation it should use the main queue", kind: domain.FleetKind, reason: domain.EventReasonDeviceLabelReconciliationRequested, withLabelSync: true},
		{name: "When update details are absent it should use the main queue", kind: domain.DeviceKind, reason: domain.EventReasonResourceUpdated, withLabelSync: true},
		{name: "When a label sync publisher is absent it should retain main queue delivery", kind: domain.DeviceKind, reason: domain.EventReasonDeviceLabelReconciliationRequested},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := domain.Event{Reason: tt.reason, InvolvedObject: domain.ObjectReference{Kind: string(tt.kind), Name: "device"}}
			if tt.fields != nil {
				var details domain.EventDetails
				require.NoError(t, details.FromResourceUpdatedDetails(domain.ResourceUpdatedDetails{DetailType: domain.ResourceUpdated, UpdatedFields: tt.fields}))
				event.Details = &details
			}
			taskProducer, labelSyncProducer := &recordingProducer{}, &recordingProducer{}
			var opts []ClientOption
			if tt.withLabelSync {
				opts = append(opts, WithLabelSyncPublisher(labelSyncProducer))
			}
			client := NewWorkerClient(taskProducer, logrus.New(), opts...).(ReliableWorkerClient)
			orgID := uuid.New()
			require.NoError(t, client.EmitEventWithError(context.Background(), orgID, &event))
			payloads := taskProducer.payloads
			if tt.wantLabelSync {
				require.Empty(t, taskProducer.payloads)
				payloads = labelSyncProducer.payloads
			} else {
				require.Empty(t, labelSyncProducer.payloads)
			}
			require.Len(t, payloads, 1)
			var got EventWithOrgId
			require.NoError(t, json.Unmarshal(payloads[0], &got))
			require.Equal(t, orgID, got.OrgId)
			require.Equal(t, event, got.Event)
		})
	}
}

func TestLabelSyncQueuePublisherAndFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	producer := queues.NewMockQueueProducer(ctrl)
	ctx := context.Background()
	provider.EXPECT().NewQueueProducer(ctx, consts.DeviceLabelSyncTaskQueue).Return(producer, nil)
	publisher, err := LabelSyncQueuePublisher(ctx, provider)
	require.NoError(t, err)
	producer.EXPECT().Enqueue(ctx, gomock.Any(), gomock.Any()).Return(errors.New("label sync queue unavailable"))
	event := domain.Event{Reason: domain.EventReasonDeviceLabelReconciliationRequested, InvolvedObject: domain.ObjectReference{Kind: domain.DeviceKind}}
	mainProducer := &recordingProducer{}
	client := NewWorkerClient(mainProducer, logrus.New(), WithLabelSyncPublisher(publisher)).(ReliableWorkerClient)
	require.ErrorContains(t, client.EmitEventWithError(ctx, uuid.New(), &event), "label sync queue unavailable")
	require.Empty(t, mainProducer.payloads)
}
