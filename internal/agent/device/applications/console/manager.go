package console

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"google.golang.org/grpc/metadata"
)

// sanitizeGrpcMetadataValue replaces every byte outside the printable ASCII range
// (0x20-0x7E) with '?'. gRPC-Go rejects outgoing metadata values containing such bytes
// for any key that doesn't end in "-bin" (see
// google.golang.org/grpc/internal/metadata.ValidatePair), which would otherwise make
// Stream() itself fail for any resolveErr whose text contains e.g. a newline or a
// non-ASCII app name, silently losing the specific error and degrading to a generic
// timeout on the server side instead.
func sanitizeGrpcMetadataValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E {
			return '?'
		}
		return r
	}, s)
}

// Session is the per-console-type I/O interface. Each app type (VM, container, …)
// provides its own implementation.
type Session interface {
	Run(ctx context.Context, streamClient grpc_v1.RouterService_StreamClient)
}

// AppConsoleResolver resolves an app name and console type into a ready-to-run
// Session, or returns an error if the combination is not supported.
type AppConsoleResolver interface {
	ResolveConsole(appName, consoleType string) (Session, error)
}

// ResolverFunc is a func adapter for AppConsoleResolver (like http.HandlerFunc).
// It allows callers to pass an unexported method as a resolver without exposing
// an exported method on their own type.
type ResolverFunc func(appName, consoleType string) (Session, error)

func (f ResolverFunc) ResolveConsole(appName, consoleType string) (Session, error) {
	return f(appName, consoleType)
}

// Manager tracks active and inactive app console sessions. It is a pure
// orchestrator: it calls the resolver to obtain a Session, opens the gRPC
// stream, and hands the stream to the Session to do the actual I/O.
// Created and owned by applications.manager.
type Manager struct {
	grpcClient grpc_v1.RouterServiceClient
	deviceName string
	resolver   AppConsoleResolver
	log        *log.PrefixLogger

	activeSessions   []*managedSession
	inactiveSessions []*managedSession
	mu               sync.Mutex
	sessionWg        sync.WaitGroup
}

// managedSession is the lifecycle wrapper held by Manager. It is separate from
// the Session interface so that session tracking does not pollute the I/O types.
type managedSession struct {
	id                string
	streamClient      grpc_v1.RouterService_StreamClient
	inactiveTimestamp time.Time
	// cancel ends this session's context, letting evict() force an early, clean shutdown
	// of a still-running session from Sync's goroutine.
	cancel context.CancelFunc
	// replaced is set by evict() before cancel() is called, so bridgeConn's teardown can
	// tell an explicit takeover apart from a normal disconnect and report it to the client.
	replaced atomic.Bool
}

func NewManager(
	grpcClient grpc_v1.RouterServiceClient,
	deviceName string,
	resolver AppConsoleResolver,
	log *log.PrefixLogger,
) *Manager {
	return &Manager{
		grpcClient: grpcClient,
		deviceName: deviceName,
		resolver:   resolver,
		log:        log,
	}
}

func (m *Manager) cleanup() {
	var result []*managedSession
	for _, s := range m.inactiveSessions {
		if s.inactiveTimestamp.Add(cleanupDuration).After(time.Now()) {
			result = append(result, s)
		}
	}
	m.inactiveSessions = result
}

func (m *Manager) add(s *managedSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	if _, exists := lo.Find(append(m.activeSessions, m.inactiveSessions...), func(ms *managedSession) bool {
		return s.id == ms.id
	}); exists {
		return false
	}
	m.activeSessions = append(m.activeSessions, s)
	return true
}

func (m *Manager) inactivate(s *managedSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	_, index, exists := lo.FindIndexOf(m.activeSessions, func(ms *managedSession) bool { return s.id == ms.id })
	if !exists {
		return
	}
	m.activeSessions = append(m.activeSessions[:index], m.activeSessions[index+1:]...)
	s.inactiveTimestamp = time.Now()
	m.inactiveSessions = append(m.inactiveSessions, s)
}

func (m *Manager) closeStream(s *managedSession) {
	m.log.Debugf("closing app console session %s", s.id)
	if s.streamClient != nil {
		_ = s.streamClient.CloseSend()
		s.streamClient = nil
	}
	m.inactivate(s)
}

// evict looks up the active session with the given ID and, if found, marks it as replaced
// and cancels its context so its own goroutine winds down and reports the reason to its
// client (see withEvictionReason/bridgeConn). Returns whether a matching session was found.
func (m *Manager) evict(sessionID string) bool {
	m.mu.Lock()
	ms, found := lo.Find(m.activeSessions, func(s *managedSession) bool { return s.id == sessionID })
	m.mu.Unlock()
	if !found {
		return false
	}
	ms.replaced.Store(true)
	ms.cancel()
	return true
}

// sendErrorOverStream makes a best-effort attempt to notify the server of a session-level
// failure before tearing the stream down. Send/CloseSend errors are intentionally ignored:
// the caller is already abandoning this stream because of an earlier failure, so there is
// nothing actionable to do if the notification itself fails other than proceed with cleanup.
func sendErrorOverStream(streamClient grpc_v1.RouterService_StreamClient, msg string) {
	if streamClient == nil {
		return
	}
	_ = streamClient.Send(&grpc_v1.StreamRequest{Error: msg})
	_ = streamClient.CloseSend()
}

// Start is the entry point called by syncConsole for each annotation entry.
// It resolves the app console, opens the gRPC stream, and runs the session.
func (m *Manager) Start(ctx context.Context, entry v1beta1.DeviceRemoteSession) {
	// sessionCtx/sessionCancel let evict() signal "wind down" without tearing down the gRPC
	// stream's own transport. The stream is deliberately created from ctx (this function's
	// parameter, canceled only on agent shutdown), not from sessionCtx: canceling the same
	// context that backs the stream would race the server's rejection of further Send()
	// calls against bridgeConn's attempt to deliver one last "replaced" notice, and lose it
	// most of the time. Keeping them separate lets bridgeConn close the local conn first
	// (via sessionCtx), send the reason over the still-live stream, and only then CloseSend.
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	ms := &managedSession{id: entry.SessionID, cancel: sessionCancel}
	if !m.add(ms) {
		return
	}
	defer m.closeStream(ms)

	if m.grpcClient == nil {
		m.log.Errorf("gRPC client not available for app console session %s", entry.SessionID)
		return
	}
	if m.resolver == nil {
		m.log.Errorf("app console resolver not configured for session %s", entry.SessionID)
		return
	}

	session, resolveErr := m.resolver.ResolveConsole(entry.AppName, entry.ConsoleType)

	metadataPairs := []string{
		consts.GrpcSessionIDKey, entry.SessionID,
		consts.GrpcClientNameKey, m.deviceName,
		consts.GrpcAppNameKey, entry.AppName,
	}
	// Report resolve failures via metadata (read by the server before any message
	// exchange) rather than over the stream body, so the server can fail the session
	// before the client's connection is upgraded instead of only after.
	if resolveErr != nil {
		metadataPairs = append(metadataPairs, consts.GrpcSessionErrorKey, sanitizeGrpcMetadataValue(resolveErr.Error()))
	} else {
		metadataPairs = append(metadataPairs, consts.GrpcSelectedProtocolKey, entry.ConsoleType)
	}
	streamCtx := metadata.AppendToOutgoingContext(ctx, metadataPairs...)
	streamClient, err := m.grpcClient.Stream(streamCtx)
	if err != nil {
		m.log.Errorf("error creating app console stream for session %s: %v", entry.SessionID, err)
		return
	}
	ms.streamClient = streamClient

	if resolveErr != nil {
		m.log.Errorf("cannot open console for app %s: %v", entry.AppName, resolveErr)
		return
	}

	session.Run(withEvictionReason(sessionCtx, &ms.replaced), streamClient)
}

// Sync reads the DeviceAnnotationRemoteSession annotation and starts a goroutine
// for each entry with a non-empty appName that is not already tracked.
func (m *Manager) Sync(ctx context.Context, device *v1beta1.Device) {
	m.log.Debug("Syncing app console sessions")

	if device.Metadata.Annotations == nil {
		return
	}

	val, ok := (*device.Metadata.Annotations)[v1beta1.DeviceAnnotationRemoteSession]
	if !ok || val == "" {
		return
	}

	var sessions []v1beta1.DeviceRemoteSession
	if err := json.Unmarshal([]byte(val), &sessions); err != nil {
		m.log.Errorf("failed to parse remote session annotation: %v", err)
		return
	}

	for _, entry := range sessions {
		if entry.AppName == "" || entry.SessionID == "" {
			continue
		}
		// A --force takeover names the session it is replacing explicitly, so this is the
		// only condition that ever tears down a still-active session here. A session ID
		// simply vanishing from the annotation (e.g. a normal close followed by a fast
		// reopen) must never be treated as a replacement.
		if entry.ReplacesSessionID != "" {
			m.evict(entry.ReplacesSessionID)
		}
		e := entry
		m.sessionWg.Add(1)
		go func() {
			defer m.sessionWg.Done()
			m.Start(ctx, e)
		}()
	}
}

// Wait blocks until all active sessions have finished. Called during shutdown.
func (m *Manager) Wait() {
	m.sessionWg.Wait()
}
