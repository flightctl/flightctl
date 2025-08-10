package thread

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

// Thread provides a background, periodic thread, which invokes the given function every supplied interval.
//
// Sample usage:
//
//	monitorFunc := func() {
//	    //do monitoring logic
//	}
//	monitor := thread.New(log, "Health Monitor", time.Minute*2, monitorFunc)
//	monitor.Start()
//	defer monitor.Stop()
//	....
type Thread struct {
	ctx              context.Context
	log              logrus.FieldLogger
	exec             func(context.Context)
	done             chan struct{}
	name             string
	interval         time.Duration
	lastRunStartedAt time.Time
}

func New(ctx context.Context, log logrus.FieldLogger, name string, interval time.Duration, exec func(context.Context)) *Thread {
	return &Thread{
		ctx:      ctx,
		log:      log,
		exec:     exec,
		name:     name,
		done:     make(chan struct{}),
		interval: interval,
	}
}

// Start thread
func (t *Thread) Start() {
	t.log.Infof("Started %s", t.name)
	t.lastRunStartedAt = time.Now()
	go t.loop()
}

// Stop thread
func (t *Thread) Stop() {
	t.log.Infof("Stopping %s", t.name)
	t.done <- struct{}{}
	<-t.done
	t.log.Infof("Stopped %s", t.name)
}

func (t *Thread) LastRunStartedAt() time.Time {
	return t.lastRunStartedAt
}

func (t *Thread) Name() string {
	return t.name
}

func (t *Thread) loop() {
	defer close(t.done)
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.done:
			return
		case <-ticker.C:
			ctx, span := tracing.StartSpan(t.ctx, "flightctl/thread", t.name, trace.WithNewRoot())
			t.lastRunStartedAt = time.Now()
			t.exec(ctx)
			span.End()
		}
	}
}
