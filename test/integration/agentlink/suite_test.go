// Package agentlink_test exercises device log retrieval end to end over a
// live agent link.
//
// What makes this an integration test rather than a larger unit test is that
// nothing between the two endpoints is stubbed: a real WebSocket connection
// carries real encoded envelopes, both ends run the shared link machinery with
// their production roles and handler allowlists, and the agent answers through
// the same journal scan the device uses. The service originates and the agent
// responds, which is the only direction this method is implemented in.
//
// Two things are deliberately outside it. The route's own handshake refusals
// and its dispatch registration belong to the service-side route and are
// covered where that code lives, so the connection here is established without
// mTLS or the agent middleware chain. And journalctl itself is replaced by a
// fake, because a build container has no populated system journal to read and
// a test that depended on one would assert nothing repeatable.
package agentlink_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentclient "github.com/flightctl/flightctl/internal/agent/client"
	devicelink "github.com/flightctl/flightctl/internal/agent/device/link"
	"github.com/flightctl/flightctl/internal/agent/device/logs"
	"github.com/flightctl/flightctl/internal/agentlink/devicelogs"
	"github.com/flightctl/flightctl/internal/agentlink/link"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestAgentLink(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Link Device Logs Suite")
}

// eventTimeout bounds every wait in this suite. Nothing here depends on a
// timer firing, so a wait that reaches it is a failure and not slowness.
const eventTimeout = 10 * time.Second

// fakeJournalctl stands in for the journalctl binary on the device.
//
// Only the bounded-output call is implemented, because that is the only one
// the journal source makes: every invocation it builds carries a working
// buffer bound.
type fakeJournalctl struct {
	mu       sync.Mutex
	stdout   string
	stderr   string
	exitCode int
	// answer, when set, renders a reply from the argv, so a spec can tell two
	// concurrent requests apart by what each one asked for.
	answer func(args []string) (stdout, stderr string, exitCode int)
	// hold, when set, blocks the invocation until it is closed or the context
	// ends. It is how a spec gets a request to still be running.
	hold  chan struct{}
	calls [][]string

	// abandoned counts invocations that ended because their context was
	// cancelled. It is the evidence that cancelling a request really stopped
	// the device-side work rather than only fixing the outcome locally.
	abandoned atomic.Int64
}

func (f *fakeJournalctl) answerWith(stdout, stderr string, exitCode int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stdout, f.stderr, f.exitCode = stdout, stderr, exitCode
}

func (f *fakeJournalctl) answerFrom(fn func(args []string) (string, string, int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = fn
}

func (f *fakeJournalctl) blockUntilReleased() (release func()) {
	f.mu.Lock()
	hold := make(chan struct{})
	f.hold = hold
	f.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { close(hold) }) }
}

// callCount is how many invocations have begun. An invocation is recorded
// before it blocks, so this is "started", not "finished".
func (f *fakeJournalctl) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// argsOf returns the argv of the n-th invocation, so a spec can assert that a
// filter actually reached journalctl rather than only that the result looked
// right.
func (f *fakeJournalctl) argsOf(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.calls) {
		return nil
	}
	return append([]string(nil), f.calls[n]...)
}

func (f *fakeJournalctl) ExecuteWithBoundedOutputFromDir(ctx context.Context, _ string, _ string, args []string, _ int, _ ...string) (string, string, int) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	stdout, stderr, exitCode, hold, answer := f.stdout, f.stderr, f.exitCode, f.hold, f.answer
	f.mu.Unlock()

	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			f.abandoned.Add(1)
			return "", "context canceled", 1
		}
	}
	if answer != nil {
		return answer(args)
	}
	return stdout, stderr, exitCode
}

func (f *fakeJournalctl) CommandContext(context.Context, string, ...string) *exec.Cmd {
	panic("the journal source never builds a command directly")
}

func (f *fakeJournalctl) Execute(string, ...string) (string, string, int) {
	panic("the journal source never executes without a context")
}

func (f *fakeJournalctl) ExecuteWithContext(context.Context, string, ...string) (string, string, int) {
	panic("the journal source never executes without a working-buffer bound")
}

func (f *fakeJournalctl) ExecuteWithContextFromDir(context.Context, string, string, []string, ...string) (string, string, int) {
	panic("the journal source never executes without a working-buffer bound")
}

// linkPair is one live link with both endpoints running.
type linkPair struct {
	// service is the endpoint in the service role, which is the one that
	// originates a log request.
	service *link.Endpoint
	// agent is the endpoint in the agent role. A spec reaches it only to show
	// what happens when the agent originates, which no production path does.
	agent *link.Endpoint

	journalctl *fakeJournalctl

	// serviceEnded and agentEnded carry each Serve's return value.
	serviceEnded <-chan error
	agentEnded   <-chan error

	stopService context.CancelFunc
	stopAgent   context.CancelFunc
}

// newLinkPair brings up a link with the agent serving its production method
// allowlist and the service serving none, which is what each side implements.
func newLinkPair(opts ...logs.JournalOption) *linkPair {
	GinkgoHelper()

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	prefixed := log.NewPrefixLogger("")
	prefixed.SetOutput(io.Discard)

	journalctl := &fakeJournalctl{}
	journal := logs.NewJournal(prefixed, agentclient.NewJournalctl(journalctl, v1beta1.Username("")), opts...)

	serviceCtx, stopService := context.WithCancel(context.Background())
	agentCtx, stopAgent := context.WithCancel(context.Background())

	serviceReady := make(chan *link.Endpoint, 1)
	serviceEnded := make(chan error, 1)

	upgrader := websocket.Upgrader{Subprotocols: []string{protocol.Subprotocol}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serviceEnded <- err
			return
		}
		// Handlers is nil on this side on purpose: the service implements no
		// method in the responder role, so an agent-originated request --
		// including device.logs.get -- is answered with MethodNotSupported.
		serviceEnded <- link.Serve(serviceCtx, conn, link.Config{
			Role:           protocol.OriginService,
			Handlers:       nil,
			Log:            logger,
			ShutdownBudget: 250 * time.Millisecond,
			OnConnect:      func(e *link.Endpoint) { serviceReady <- e },
		})
	}))

	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(conn.Subprotocol()).To(Equal(protocol.Subprotocol))

	agentReady := make(chan *link.Endpoint, 1)
	agentEnded := make(chan error, 1)
	go func() {
		agentEnded <- link.Serve(agentCtx, conn, link.Config{
			Role:           protocol.OriginAgent,
			Handlers:       devicelink.Handlers(journal, prefixed),
			Log:            logger,
			ShutdownBudget: 250 * time.Millisecond,
			OnConnect:      func(e *link.Endpoint) { agentReady <- e },
		})
	}()

	pair := &linkPair{
		journalctl:   journalctl,
		serviceEnded: serviceEnded,
		agentEnded:   agentEnded,
		stopService:  stopService,
		stopAgent:    stopAgent,
	}
	Eventually(serviceReady, eventTimeout).Should(Receive(&pair.service))
	Eventually(agentReady, eventTimeout).Should(Receive(&pair.agent))

	DeferCleanup(func() {
		stopAgent()
		stopService()
		srv.Close()
	})
	return pair
}

// outcome is everything one request produced.
type outcome struct {
	payloads []devicelogs.Payload
	status   *protocol.Status
}

// content is the log text the caller reconstitutes. Message boundaries are
// invisible to it, which is the point: concatenating is all a reader does.
func (o outcome) content() string {
	var b strings.Builder
	for _, p := range o.payloads {
		b.WriteString(p.Content)
	}
	return b.String()
}

// drain consumes a request's events to its terminal result. A channel that
// closes without one never happens, so reaching that is a failure.
func drain(o *link.Originated) outcome {
	GinkgoHelper()

	var result outcome
	deadline := time.After(eventTimeout)
	for {
		select {
		case ev, ok := <-o.Events():
			if !ok {
				Expect(result.status).ToNot(BeNil(), "the event channel closed without a terminal result")
				return result
			}
			switch ev.Kind {
			case protocol.KindData:
				payload, err := devicelogs.DecodePayload(ev.Payload)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.status).To(BeNil(), "data arrived after the outcome was fixed")
				result.payloads = append(result.payloads, *payload)
			case protocol.KindResult:
				Expect(result.status).To(BeNil(), "a request's outcome is fixed exactly once")
				result.status = ev.Status
			default:
				Fail("unexpected event kind " + string(ev.Kind))
			}
		case <-deadline:
			Fail("timed out waiting for a request to reach a terminal result")
		}
	}
}

// originate sends one device.logs.get from the service without waiting for it.
func (p *linkPair) originate(id string, params *devicelogs.Params) *link.Originated {
	GinkgoHelper()

	raw, err := devicelogs.EncodeParams(params)
	Expect(err).ToNot(HaveOccurred())

	originated, err := p.service.Originate(id, devicelogs.Method, raw, nil)
	Expect(err).ToNot(HaveOccurred())
	return originated
}

// request originates one device.logs.get from the service and drains it.
func (p *linkPair) request(id string, params *devicelogs.Params) outcome {
	GinkgoHelper()
	return drain(p.originate(id, params))
}

// journalParams is the common case: a journal read with no filters.
func journalParams() *devicelogs.Params {
	return &devicelogs.Params{Source: devicelogs.SourceJournal, Journal: &devicelogs.JournalParams{}}
}
