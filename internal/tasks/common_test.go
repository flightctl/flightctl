package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestEvent(kind domain.ResourceKind, reason domain.EventReason, name string) domain.Event {
	return domain.Event{
		InvolvedObject: domain.ObjectReference{
			Kind: string(kind),
			Name: name,
		},
		Reason: reason,
	}
}

func createTestFleet(name string, rolloutPolicy *domain.RolloutPolicy) *domain.Fleet {
	fleetName := name
	generation := int64(1)

	return &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:       &fleetName,
			Generation: &generation,
		},
		Spec: domain.FleetSpec{
			RolloutPolicy: rolloutPolicy,
			Template: struct {
				Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
				Spec     domain.DeviceSpec  `json:"spec"`
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "test-image:latest",
					},
				},
			},
		},
	}
}

func TestWithTimeoutIgnoringParentDeadline(t *testing.T) {
	t.Run("child not expired when parent deadline passed", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		time.Sleep(time.Millisecond)
		require.Error(t, parent.Err())
		require.True(t, errors.Is(parent.Err(), context.DeadlineExceeded))

		iterCtx, cancelIter := WithTimeoutIgnoringParentDeadline(parent, time.Hour)
		defer cancelIter()
		assert.NoError(t, iterCtx.Err())
	})

	t.Run("child canceled when parent explicitly canceled", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		cancelParent()

		iterCtx, cancelIter := WithTimeoutIgnoringParentDeadline(parent, time.Hour)
		defer cancelIter()

		select {
		case <-iterCtx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("expected iteration context canceled after parent cancel")
		}
		assert.True(t, errors.Is(iterCtx.Err(), context.Canceled))
	})
}
