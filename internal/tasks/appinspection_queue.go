package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	inspectionservice "github.com/flightctl/flightctl/internal/service/appinspection"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const inspectionWakeupBatch = 64

type inspectionWakeupKind string

const (
	inspectionWork   inspectionWakeupKind = "work"
	inspectionResume inspectionWakeupKind = "resume"
)

type inspectionWakeup struct {
	Kind  inspectionWakeupKind `json:"kind"`
	OrgID uuid.UUID            `json:"orgID"`
	ID    uuid.UUID            `json:"id"`
}

func decodeInspectionWakeup(payload []byte) (inspectionWakeup, error) {
	var message struct {
		Kind  inspectionWakeupKind `json:"kind"`
		OrgID *uuid.UUID           `json:"orgID"`
		ID    uuid.UUID            `json:"id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return inspectionWakeup{}, errors.New("invalid application inspection wakeup")
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF || message.OrgID == nil || message.ID == uuid.Nil || (message.Kind != inspectionWork && message.Kind != inspectionResume) {
		return inspectionWakeup{}, errors.New("invalid application inspection wakeup")
	}
	return inspectionWakeup{Kind: message.Kind, OrgID: *message.OrgID, ID: message.ID}, nil
}

type InspectionWakeups struct {
	store    *inspectionstore.Store
	producer queues.QueueProducer
}

func NewInspectionWakeups(ctx context.Context, provider queues.Provider, store *inspectionstore.Store) (*InspectionWakeups, error) {
	producer, err := provider.NewQueueProducer(ctx, consts.ApplicationInspectionQueue)
	if err != nil {
		return nil, err
	}
	return &InspectionWakeups{store: store, producer: producer}, nil
}

func (q *InspectionWakeups) Close() { q.producer.Close() }

func (q *InspectionWakeups) publish(ctx context.Context, kind inspectionWakeupKind, orgID, id uuid.UUID) error {
	payload, err := json.Marshal(inspectionWakeup{Kind: kind, OrgID: orgID, ID: id})
	if err != nil {
		return err
	}
	return q.producer.Enqueue(ctx, payload, queues.DurableWakeupTimestamp)
}

func (q *InspectionWakeups) publishBatch(ctx context.Context, kind inspectionWakeupKind, orgID uuid.UUID, ids []uuid.UUID) error {
	for _, id := range ids {
		if err := q.publish(ctx, kind, orgID, id); err != nil {
			return err
		}
	}
	return nil
}

type InspectionRecoveryCursor struct {
	Work   uuid.UUID
	Resume uuid.UUID
}

// Recover republishes one bounded page per phase. Cursors cycle independently
// so recurrent failures in one phase cannot starve other work.
func (q *InspectionWakeups) Recover(ctx context.Context, orgID uuid.UUID, cursor InspectionRecoveryCursor) (InspectionRecoveryCursor, error) {
	work, err := q.store.ListRecoverableWork(ctx, orgID, cursor.Work, inspectionWakeupBatch)
	if err != nil {
		return cursor, err
	}
	if err := q.publishBatch(ctx, inspectionWork, orgID, work); err != nil {
		return cursor, err
	}
	cursor.Work = nextInspectionPage(work)
	ready, err := q.store.ListReadyContinuations(ctx, orgID, cursor.Resume, inspectionWakeupBatch)
	if err != nil {
		return cursor, err
	}
	if err := q.publishBatch(ctx, inspectionResume, orgID, ready); err != nil {
		return cursor, err
	}
	cursor.Resume = nextInspectionPage(ready)
	return cursor, nil
}

func nextInspectionPage(ids []uuid.UUID) uuid.UUID {
	if len(ids) == inspectionWakeupBatch {
		return ids[len(ids)-1]
	}
	return uuid.Nil
}

// LaunchInspectionConsumer reserves one consumer thread for inspection and
// resume so registry or Helm work cannot block the ordinary event consumers.
func LaunchInspectionConsumer(ctx context.Context, provider queues.Provider, q *InspectionWakeups, coordinator *inspectionservice.Coordinator, reload inspectionservice.ReloadApplication, resume func(context.Context, uuid.UUID, uuid.UUID) error, timeout time.Duration) error {
	consumer, err := provider.NewQueueConsumer(ctx, consts.ApplicationInspectionQueue)
	if err != nil {
		return err
	}
	handle := func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
		message, err := decodeInspectionWakeup(payload)
		if err == nil {
			if message.Kind == inspectionWork {
				err = q.process(ctx, coordinator, reload, message)
			} else {
				resumeCtx, cancel := context.WithTimeout(ctx, timeout)
				err = resume(resumeCtx, message.OrgID, message.ID)
				cancel()
			}
		}
		if err != nil {
			log.WithError(err).Warn("application inspection wakeup did not complete")
		}
		// The database also handles failures and lost acknowledgements. These
		// wakeups do not enter the event queue's retry or checkpoint lifecycle.
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), AckTimeout)
		defer cancel()
		return consumer.Complete(ackCtx, entryID, nil, nil)
	}
	if err := consumer.Consume(ctx, handle); err != nil {
		consumer.Close()
		return err
	}
	return nil
}

func (q *InspectionWakeups) process(ctx context.Context, coordinator *inspectionservice.Coordinator, reload inspectionservice.ReloadApplication, message inspectionWakeup) error {
	progress, err := coordinator.Process(ctx, message.OrgID, message.ID, reload)
	if err != nil {
		return err
	}
	publishCtx, cancel := context.WithTimeout(ctx, AckTimeout)
	defer cancel()
	if progress.StaleWaiter != uuid.Nil {
		return errors.Join(q.publish(publishCtx, inspectionResume, message.OrgID, progress.StaleWaiter),
			q.publish(publishCtx, inspectionWork, message.OrgID, progress.RetryWork))
	}
	if progress.InspectionID != uuid.Nil {
		ids, err := q.store.ListReadyForInspection(publishCtx, message.OrgID, progress.InspectionID, uuid.Nil, inspectionWakeupBatch)
		if err != nil {
			return err
		}
		return q.publishBatch(publishCtx, inspectionResume, message.OrgID, ids)
	}
	return nil
}
