package agentlink_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/logs"
	"github.com/flightctl/flightctl/internal/agentlink/devicelogs"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Device log retrieval over the agent link", func() {
	Context("when the service asks a connected agent for a journal snapshot", func() {
		It("should return the rendered lines and reach the device with the requested filters", func() {
			pair := newLinkPair()
			pair.journalctl.answerWith("Nov 17 09:14:02 dev-01 agent[912]: syncing spec v42\n"+
				"Nov 17 09:14:03 dev-01 agent[912]: spec v42 applied\n", "", 0)

			since := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
			result := pair.request("req-filters", &devicelogs.Params{
				Source: devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{
					Units:     []string{"flightctl-agent.service", "crio.*"},
					Priority:  "warning",
					Boot:      "current",
					SinceTime: &since,
				},
			})

			Expect(result.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(result.content()).To(Equal(
				"Nov 17 09:14:02 dev-01 agent[912]: syncing spec v42\n" +
					"Nov 17 09:14:03 dev-01 agent[912]: spec v42 applied\n"))

			// The filters are data in an argv array, never a shell string, and
			// the priority travels as its syslog number rather than by name.
			args := pair.journalctl.argsOf(0)
			Expect(args).To(ContainElements("-u", "flightctl-agent.service"))
			Expect(args).To(ContainElements("-u", "crio.*"))
			Expect(args).To(ContainElements("-p", "4"))
			Expect(args).To(ContainElements("-b", "0"))
			Expect(args).To(ContainElement("--since"))
			Expect(args).To(ContainElement("2026-11-17 09:00:00.000000 UTC"))
			Expect(args).To(ContainElement("--quiet"))
		})

		It("should page the result in rendered lines rather than journal entries", func() {
			pair := newLinkPair()
			// One entry rendering to three lines, which is why a journalctl
			// entry budget cannot implement the window on its own.
			pair.journalctl.answerWith("a-1\nb-1\nb-2\nb-3\nc-1\n", "", 0)

			tail := func(tailLines int64, offset *int64) outcome {
				return pair.request(fmt.Sprintf("req-page-%d-%v", tailLines, offset), &devicelogs.Params{
					Source:     devicelogs.SourceJournal,
					Journal:    &devicelogs.JournalParams{},
					TailLines:  tailLines,
					LineOffset: offset,
				})
			}

			two := int64(2)
			Expect(tail(2, nil).content()).To(Equal("b-3\nc-1\n"))
			Expect(tail(2, &two).content()).To(Equal("b-1\nb-2\n"))

			head := pair.request("req-head", &devicelogs.Params{
				Source:     devicelogs.SourceJournal,
				Journal:    &devicelogs.JournalParams{},
				LineOffset: &two,
			})
			Expect(head.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(head.content()).To(Equal("b-2\nb-3\nc-1\n"))
		})

		It("should split a large snapshot across messages without changing its content", func() {
			pair := newLinkPair()

			var source strings.Builder
			for i := 0; i < 6000; i++ {
				fmt.Fprintf(&source, "%06d %s\n", i, strings.Repeat("y", 80))
			}
			pair.journalctl.answerWith(source.String(), "", 0)

			result := pair.request("req-large", journalParams())

			Expect(result.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(len(result.payloads)).To(BeNumerically(">", 1),
				"a snapshot this size must cross the agent's chunk bound")
			Expect(result.content()).To(Equal(source.String()))
		})

		It("should answer an empty selection with a success carrying no data message", func() {
			pair := newLinkPair()
			pair.journalctl.answerWith("", "", 0)

			result := pair.request("req-empty", journalParams())

			Expect(result.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(result.payloads).To(BeEmpty())
		})
	})

	Context("when the retrieval cannot succeed", func() {
		It("should report an absent boot distinctly from a device fault", func() {
			pair := newLinkPair()
			pair.journalctl.answerWith("", "Failed to look up boot -1: No such boot ID in journal", 1)

			result := pair.request("req-boot", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{Boot: "previous"},
			})

			Expect(result.status.Reason).To(Equal(protocol.ReasonBootUnavailable))
			Expect(result.payloads).To(BeEmpty())
		})

		It("should report the agent's own scan budget as a device timeout and send no partial page", func() {
			pair := newLinkPair(logs.WithScanBudget(100 * time.Millisecond))
			release := pair.journalctl.blockUntilReleased()
			DeferCleanup(release)

			result := pair.request("req-budget", journalParams())

			Expect(result.status.Reason).To(Equal(protocol.ReasonDeviceTimeout))
			Expect(result.payloads).To(BeEmpty(),
				"a budget reached before the page was assembled fails loudly rather than returning what it had")
		})

		It("should fail a request whose parameters it will not act on, leaving the link usable", func() {
			pair := newLinkPair()
			pair.journalctl.answerWith("never-read\n", "", 0)

			// '[' and ']' are outside the accepted unit charset, so a
			// character-class pattern is refused rather than passed through to
			// match literally and silently return nothing.
			bad := pair.request("req-bad-unit", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{Units: []string{"crio[0-9].service"}},
			})
			Expect(bad.status.Reason).To(Equal(protocol.ReasonRetrievalFailed))
			Expect(bad.status.Message).To(ContainSubstring("unsupported character"))
			Expect(bad.payloads).To(BeEmpty())
			Expect(pair.journalctl.argsOf(0)).To(BeNil(), "a refused request must start no source work")

			// The refusal was request-scoped: the link carries on.
			good := pair.request("req-after-bad", journalParams())
			Expect(good.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(good.content()).To(Equal("never-read\n"))
		})

		It("should refuse a capability it lacks without claiming the method is absent", func() {
			pair := newLinkPair()
			pair.journalctl.answerWith("ignored\n", "", 0)

			follow := pair.request("req-follow", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{},
				Follow:  true,
			})

			// MethodNotSupported would say the agent does not implement
			// device.logs.get at all, which is a capability verdict and false.
			Expect(follow.status.Reason).To(Equal(protocol.ReasonRetrievalFailed))
			Expect(follow.status.Message).To(ContainSubstring("follow is not implemented"))
		})
	})

	Context("when a method is not implemented in the role the request asks for", func() {
		It("should answer a service-originated unknown method with one MethodNotSupported", func() {
			pair := newLinkPair()

			originated, err := pair.service.Originate("req-unknown", "device.config.apply", []byte(`{}`), nil)
			Expect(err).ToNot(HaveOccurred())
			result := drain(originated)

			Expect(result.status.Reason).To(Equal(protocol.ReasonMethodNotSupported))
			Expect(result.payloads).To(BeEmpty())

			// The link is untouched: a sibling request still works.
			pair.journalctl.answerWith("still-here\n", "", 0)
			Expect(pair.request("req-after-unknown", journalParams()).status.Reason).To(Equal(protocol.ReasonOK))
		})

		It("should refuse device.logs.get asked of the service, which would read control-plane host logs", func() {
			pair := newLinkPair()

			raw, err := devicelogs.EncodeParams(journalParams())
			Expect(err).ToNot(HaveOccurred())

			// The allowlist is keyed by method and responder role. This is the
			// same method name in the other direction, and no endpoint
			// implements it that way round.
			originated, err := pair.agent.Originate("ag-logs-1", devicelogs.Method, raw, nil)
			Expect(err).ToNot(HaveOccurred())
			result := drain(originated)

			Expect(result.status.Reason).To(Equal(protocol.ReasonMethodNotSupported))
			Expect(result.payloads).To(BeEmpty())
		})
	})

	Context("when several requests share one link", func() {
		It("should run them concurrently and give each its own result", func() {
			pair := newLinkPair()
			// Each reply names the unit it was asked for, so neither request
			// can be satisfied by the other's answer.
			pair.journalctl.answerFrom(func(args []string) (string, string, int) {
				for i, arg := range args {
					if arg == "-u" && i+1 < len(args) {
						return "logs for " + args[i+1] + "\n", "", 0
					}
				}
				return "", "no unit", 1
			})
			release := pair.journalctl.blockUntilReleased()

			first := pair.originate("req-a", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{Units: []string{"a.service"}},
			})
			second := pair.originate("req-b", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{Units: []string{"b.service"}},
			})

			// Both are in flight on the one link before either can finish.
			Eventually(pair.journalctl.callCount, eventTimeout).Should(Equal(2))
			release()

			firstResult, secondResult := drain(first), drain(second)
			Expect(firstResult.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(secondResult.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(firstResult.content()).To(Equal("logs for a.service\n"))
			Expect(secondResult.content()).To(Equal("logs for b.service\n"))
		})

		It("should stop only the cancelled request's device work and leave its siblings running", func() {
			pair := newLinkPair()
			pair.journalctl.answerFrom(func(args []string) (string, string, int) {
				for i, arg := range args {
					if arg == "-u" && i+1 < len(args) {
						return "logs for " + args[i+1] + "\n", "", 0
					}
				}
				return "", "no unit", 1
			})
			release := pair.journalctl.blockUntilReleased()
			DeferCleanup(release)

			cancelled := pair.originate("req-cancelled", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{Units: []string{"doomed.service"}},
			})
			survivor := pair.originate("req-survivor", &devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Journal: &devicelogs.JournalParams{Units: []string{"kept.service"}},
			})
			Eventually(pair.journalctl.callCount, eventTimeout).Should(Equal(2))

			Expect(cancelled.Cancel(protocol.ReasonCancelled, "client disconnected")).To(Succeed())

			// The cancel reached the device and stopped that invocation. The
			// sibling's is still blocked, so exactly one was abandoned.
			Eventually(func() int64 { return pair.journalctl.abandoned.Load() }, eventTimeout).Should(Equal(int64(1)))
			Expect(drain(cancelled).status.Reason).To(Equal(protocol.ReasonCancelled))

			release()
			survived := drain(survivor)
			Expect(survived.status.Reason).To(Equal(protocol.ReasonOK))
			Expect(survived.content()).To(Equal("logs for kept.service\n"))
		})
	})

	Context("when the link ends under a running request", func() {
		It("should fail that request explicitly rather than leave it waiting", func() {
			pair := newLinkPair()
			release := pair.journalctl.blockUntilReleased()
			DeferCleanup(release)

			running := pair.originate("req-interrupted", journalParams())
			Eventually(pair.journalctl.callCount, eventTimeout).Should(Equal(1))

			// The agent goes away. Its ordered shutdown finishes or fails each
			// running request while the connection is still writable and sends
			// the Close frame last.
			pair.stopAgent()

			result := drain(running)
			Expect(result.status).ToNot(BeNil(), "a request's end is never inferred from the connection closing")
			// Which non-OK reason arrives depends on whether the agent's own
			// result won the race with the link ending. Both are correct and
			// neither is a success: a clean Close is never a request's OK.
			Expect(result.status.Reason).ToNot(Equal(protocol.ReasonOK))
			Expect(result.payloads).To(BeEmpty())

			Eventually(pair.agentEnded, eventTimeout).Should(Receive())
		})
	})
})
