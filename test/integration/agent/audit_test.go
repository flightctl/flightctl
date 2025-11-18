package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/spec/audit"
	"github.com/flightctl/flightctl/test/harness"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent Audit Log", func() {
	var (
		h *harness.TestHarness
	)

	BeforeEach(func() {
		var err error
		testDirPath := GinkgoT().TempDir()
		goRoutineErrorHandler := func(err error) {
			if err != nil {
				Fail(err.Error())
			}
		}
		h, err = harness.NewTestHarness(suiteCtx, testDirPath, goRoutineErrorHandler, harness.WithAgentAudit())
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if h != nil {
			h.Cleanup()
		}
	})

	It("creates audit log file after agent starts", func() {
		// Wait for agent to initialize
		time.Sleep(2 * time.Second)

		// Verify audit log file exists
		auditLogPath := filepath.Join(h.TestDirPath, "var", "log", "flightctl", "audit.log")
		_, err := os.Stat(auditLogPath)
		Expect(err).ToNot(HaveOccurred(), "audit log file should exist")
	})

	It("logs bootstrap events correctly", func() {
		// Wait for agent to initialize
		time.Sleep(2 * time.Second)

		// Read audit log
		auditLogPath := filepath.Join(h.TestDirPath, "var", "log", "flightctl", "audit.log")
		events, err := readAuditLog(auditLogPath)
		Expect(err).ToNot(HaveOccurred())

		// Verify bootstrap events (should be at least 3: current, desired, rollback)
		Expect(len(events)).To(BeNumerically(">=", 3), "should have at least 3 bootstrap events")

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
		// Wait for agent to initialize
		time.Sleep(2 * time.Second)

		// Read audit log
		auditLogPath := filepath.Join(h.TestDirPath, "var", "log", "flightctl", "audit.log")
		events, err := readAuditLog(auditLogPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(events)).To(BeNumerically(">", 0), "should have at least one audit event")

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
		_, err = time.Parse(time.RFC3339, event.Ts)
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
		// Wait for agent to initialize
		time.Sleep(2 * time.Second)

		// Read audit log file line by line
		auditLogPath := filepath.Join(h.TestDirPath, "var", "log", "flightctl", "audit.log")
		file, err := os.Open(auditLogPath)
		Expect(err).ToNot(HaveOccurred())
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineCount := 0
		for scanner.Scan() {
			lineCount++
			line := scanner.Text()

			// Each line should be valid JSON
			var event audit.Event
			err := json.Unmarshal([]byte(line), &event)
			Expect(err).ToNot(HaveOccurred(), "line %d should be valid JSON", lineCount)
		}

		Expect(scanner.Err()).ToNot(HaveOccurred())
		Expect(lineCount).To(BeNumerically(">", 0), "should have at least one line")
	})
})

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
