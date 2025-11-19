package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/spec/audit"
	"github.com/flightctl/flightctl/test/harness"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent Audit Log", func() {
	var (
		ctx context.Context
		h   *harness.TestHarness
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		var err error
		h, err = harness.NewTestHarness(ctx, GinkgoT().TempDir(), func(err error) {
			// this inline function handles any errors that are returned from go routines
			fmt.Fprintf(os.Stderr, "Error in test harness go routine: %v\n", err)
			GinkgoWriter.Printf("Error in go routine: %v\n", err)
			GinkgoRecover()
		}, harness.WithAgentAudit())
		// check for test harness creation errors
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if h != nil {
			h.Cleanup()
		}
	})

	It("creates audit log file after agent starts", func() {
		// Wait for at least one event - this proves the file was created and is writable
		waitForAuditEvents(h, 1)
	})

	It("logs bootstrap events correctly", func() {
		// Wait for at least 3 bootstrap events to be logged
		events := waitForAuditEvents(h, 3)

		// Verify first 3 events are bootstrap events
		bootstrapEvents := events[:3]
		for _, event := range bootstrapEvents {
			Expect(event.Reason).To(Equal(audit.ReasonBootstrap), "first events should be bootstrap")
			Expect(event.OldVersion).To(BeEmpty(), "old_version should be empty for bootstrap")
			Expect(event.NewVersion).To(Equal("0"), "new_version should be '0' for bootstrap")
			Expect(event.Result).To(Equal(audit.ResultSuccess), "result should be success")
		}

		// Verify all three spec types are present
		types := map[audit.Type]bool{}
		for _, event := range bootstrapEvents {
			types[event.Type] = true
		}
		Expect(types).To(HaveKey(audit.TypeCurrent), "should have current bootstrap event")
		Expect(types).To(HaveKey(audit.TypeDesired), "should have desired bootstrap event")
		Expect(types).To(HaveKey(audit.TypeRollback), "should have rollback bootstrap event")
	})

	It("validates audit event JSON structure", func() {
		// Wait for at least one audit event
		events := waitForAuditEvents(h, 1)

		// Validate first event structure
		event := events[0]

		// Required fields should be present
		Expect(event.Ts).NotTo(BeEmpty(), "ts field should not be empty")
		Expect(event.Device).NotTo(BeEmpty(), "device field should not be empty")
		Expect(event.Result).NotTo(BeEmpty(), "result field should not be empty")
		Expect(event.Reason).NotTo(BeEmpty(), "reason field should not be empty")
		Expect(event.Type).NotTo(BeEmpty(), "type field should not be empty")
		Expect(event.AgentVersion).NotTo(BeEmpty(), "agent_version field should not be empty")

		// Validate timestamp format (RFC3339)
		_, err := time.Parse(time.RFC3339, event.Ts)
		Expect(err).ToNot(HaveOccurred(), "timestamp should be in RFC3339 format")

		// Validate reason is one of the valid values
		validReasons := []audit.Reason{
			audit.ReasonBootstrap,
			audit.ReasonSync,
			audit.ReasonUpgrade,
			audit.ReasonRollback,
			audit.ReasonRecovery,
			audit.ReasonInitialization,
		}
		Expect(validReasons).To(ContainElement(event.Reason), "reason should be a valid value")

		// Validate type is one of the valid values
		validTypes := []audit.Type{
			audit.TypeCurrent,
			audit.TypeDesired,
			audit.TypeRollback,
		}
		Expect(validTypes).To(ContainElement(event.Type), "type should be a valid value")

		// Validate result is success (failure auditing not implemented in MVP)
		Expect(event.Result).To(Equal(audit.ResultSuccess), "result should be success in MVP")
	})

	It("uses JSONL format (one JSON object per line)", func() {
		// Wait for at least one event - this validates JSONL format
		waitForAuditEvents(h, 1)
	})
})

// waitForAuditEvents waits for audit log events to be available
func waitForAuditEvents(h *harness.TestHarness, minEvents int) []audit.Event {
	auditLogPath := filepath.Join(h.TestDirPath, "var", "log", "flightctl", "audit.log")

	var events []audit.Event
	Eventually(func() int {
		var err error
		events, err = readAuditLog(auditLogPath)
		if err != nil {
			return 0
		}
		return len(events)
	}, TIMEOUT, POLLING).Should(BeNumerically(">=", minEvents), "should have at least %d audit event(s)", minEvents)

	return events
}

// readAuditLog reads the audit log file and returns parsed events
func readAuditLog(path string) ([]audit.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []audit.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event audit.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return events, nil
}
