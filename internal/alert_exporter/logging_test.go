package alert_exporter

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	organizationservice "github.com/flightctl/flightctl/internal/service/organization"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

type recordingHook struct {
	entries []*logrus.Entry
}

func (h *recordingHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *recordingHook) Fire(entry *logrus.Entry) error {
	h.entries = append(h.entries, entry)
	return nil
}

func TestWhenAlertExporterProcessesIdleCycleItShouldLogRoutineSuccessAtDebug(t *testing.T) {
	ctrl := gomock.NewController(t)
	organizationSvc := organizationservice.NewMockService(ctrl)
	checkpointSvc := checkpointservice.NewMockService(ctrl)
	eventSvc := eventservice.NewMockService(ctrl)

	organizationSvc.EXPECT().
		ListAllOrganizations(gomock.Any(), gomock.Any()).
		Return(&domain.OrganizationList{}, domain.StatusOK())
	checkpointSvc.EXPECT().
		GetDatabaseTime(gomock.Any()).
		Return(time.Date(2026, time.April, 16, 9, 44, 31, 0, time.UTC), domain.StatusOK())
	checkpointSvc.EXPECT().
		SetCheckpoint(gomock.Any(), AlertCheckpointConsumer, AlertCheckpointKey, gomock.Any()).
		Return(domain.StatusOK())

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(io.Discard)
	hook := &recordingHook{}
	logger.AddHook(hook)

	eventProcessor := NewEventProcessor(logger, organizationSvc, checkpointSvc, eventSvc)
	checkpointManager := NewCheckpointManager(logger, checkpointSvc)
	alertSender := &AlertSender{alertmanagerClient: &AlertmanagerClient{}}
	exporter := &AlertExporter{log: logger}
	checkpoint := &AlertCheckpoint{
		Version:   CurrentAlertCheckpointVersion,
		Timestamp: time.Date(2026, time.April, 16, 9, 43, 31, 0, time.UTC).Format(time.RFC3339Nano),
		Alerts:    make(map[AlertKey]map[string]*AlertInfo),
	}

	exporter.processingCycle(
		context.Background(),
		checkpointManager,
		eventProcessor,
		alertSender,
		&checkpoint,
		1,
	)

	expectedMessages := map[string]int{
		"Processing events for organizations":     1,
		"Event processing completed":              1,
		"Processing cycle completed successfully": 2,
	}
	seenMessages := make(map[string][]logrus.Level)
	for _, entry := range hook.entries {
		if _, expected := expectedMessages[entry.Message]; expected {
			seenMessages[entry.Message] = append(seenMessages[entry.Message], entry.Level)
		}
	}

	for message, expectedCount := range expectedMessages {
		levels := seenMessages[message]
		if len(levels) != expectedCount {
			t.Fatalf("expected %q to be logged %d times, got %d", message, expectedCount, len(levels))
		}
		for _, level := range levels {
			if level != logrus.DebugLevel {
				t.Errorf("expected %q to be logged at debug level, got %s", message, level)
			}
		}
	}
}
