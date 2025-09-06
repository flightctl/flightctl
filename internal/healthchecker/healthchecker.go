package healthchecker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

const (
	saveInterval         = 15 * time.Second
	maxPendingNames      = 1500
	heartbeatChannelSize = 5000
)

type Healthchecker struct {
	log   logrus.FieldLogger
	store store.Store
	input chan heartbeatRequest
	once  sync.Once
}

type HealthChecksType struct {
	util.Singleton[Healthchecker]
}

func (h *HealthChecksType) Initialize(ctx context.Context, store store.Store, log logrus.FieldLogger) {
	h.GetOrInit(&Healthchecker{
		log:   log,
		store: store,
		input: make(chan heartbeatRequest, heartbeatChannelSize),
	}).Start(ctx)
}

var HealthChecks HealthChecksType

func (h *Healthchecker) Start(ctx context.Context) {
	h.once.Do(func() {
		go h.work(ctx)
		h.log.Info("Healthchecker worker started")
	})
}

type heartbeatRequest struct {
	orgId uuid.UUID
	name  string
}

func (h *Healthchecker) save(ctx context.Context, orgId uuid.UUID, names []string) {
	if len(names) == 0 {
		return
	}
	ctx, span := startSpan(ctx, "HealthcheckDevice")
	defer span.End()
	err := h.store.Device().Healthcheck(ctx, orgId, names)
	if err != nil {
		h.log.Errorf("HealthcheckDevice failed for %s: %v", orgId, err)
	}
}

func (h *Healthchecker) work(ctx context.Context) {
	h.log.Info("starting healthcheck worker")
	defer h.log.Info("healthcheck worker stopped")
	pending := make(map[uuid.UUID][]string)

	ticker := time.NewTicker(saveInterval)
	defer ticker.Stop()
	for {
		select {
		case req := <-h.input:
			pending[req.orgId] = append(pending[req.orgId], req.name)
			if len(pending[req.orgId]) < maxPendingNames {
				continue
			}
		case <-ticker.C:
		case <-ctx.Done():
			h.log.Info("Healthcheck worker context cancelled, stopping")
			return
		}
		for orgId := range pending {
			h.save(ctx, orgId, pending[orgId])
		}
		pending = make(map[uuid.UUID][]string)
		ticker.Reset(saveInterval)
	}
}

func (h *Healthchecker) Add(ctx context.Context, orgId uuid.UUID, name string) error {
	select {
	case h.input <- heartbeatRequest{orgId: orgId, name: name}:
	default:
		h.log.Errorf("Healthcheck channel is full, dropping request for %s in org %s", name, orgId)
		return fmt.Errorf("healthcheck channel is full, dropping request for %s in org %s", name, orgId)
	}
	return nil
}

func startSpan(ctx context.Context, method string) (context.Context, trace.Span) {
	return tracing.StartSpan(ctx, "flightctl/healthcheck", method)
}
