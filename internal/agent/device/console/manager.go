package console

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/metadata"
)

const (
	cleanupDuration = 5 * time.Minute
)

type Manager struct {
	grpcClient grpc_v1.RouterServiceClient
	log        *log.PrefixLogger
	deviceName string
	watcher    spec.Watcher

	activeSessions   []*session
	inactiveSessions []*session
	executor         executer.Executer
	mu               sync.Mutex
}

type TerminalSize struct {
	Width  uint16
	Height uint16
}

func NewManager(
	grpcClient grpc_v1.RouterServiceClient,
	deviceName string,
	executor executer.Executer,
	watcher spec.Watcher,
	log *log.PrefixLogger,
) *Manager {
	return &Manager{
		grpcClient: grpcClient,
		deviceName: deviceName,
		executor:   executor,
		watcher:    watcher,
		log:        log,
	}
}

func (c *Manager) cleanup() {
	var result []*session
	for _, s := range c.inactiveSessions {
		if s.inactiveTimestamp.Add(cleanupDuration).After(time.Now()) {
			result = append(result, s)
		}
	}
	c.inactiveSessions = result
}

func (c *Manager) add(newSession *session) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanup()
	if _, exists := lo.Find(append(c.activeSessions, c.inactiveSessions...), func(s *session) bool { return newSession.id == s.id }); exists {
		return false
	}
	c.activeSessions = append(c.activeSessions, newSession)
	return true
}

func (c *Manager) inactivate(s *session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanup()
	_, index, exists := lo.FindIndexOf(c.activeSessions, func(as *session) bool { return s.id == as.id })
	if !exists {
		return
	}
	c.activeSessions = append(c.activeSessions[:index], c.activeSessions[index+1:]...)
	c.inactiveSessions = append(c.inactiveSessions, s)
}

func (c *Manager) close(s *session) {
	c.log.Debugf("closing session %s", s.id)
	_ = s.close()
	c.inactivate(s)
}

func (c *Manager) parseMetadata(metadata string) (*v1alpha1.DeviceConsoleSessionMetadata, error) {
	var ret v1alpha1.DeviceConsoleSessionMetadata
	err := json.Unmarshal([]byte(metadata), &ret)
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

func (c *Manager) selectProtocol(requestedProtocols []string) (string, error) {
	supportedProtocols := []string{
		StreamProtocolV5Name,
	}
	for _, protocol := range supportedProtocols {
		if lo.Contains(requestedProtocols, protocol) {
			return protocol, nil
		}
	}
	return "", fmt.Errorf("none of the protocols %v are supported", requestedProtocols)
}

func (c *Manager) start(ctx context.Context, dc v1alpha1.DeviceConsole) {
	s := &session{
		id:       dc.SessionID,
		executor: c.executor,
		log:      c.log,
	}
	if !c.add(s) {
		return
	}
	defer c.close(s)

	c.log.Debugf("starting session %s, metadata %s", dc.SessionID, dc.SessionMetadata)

	sessionMetadata, err := c.parseMetadata(dc.SessionMetadata)
	if err != nil {
		c.log.Errorf("failed to parse session metadata %s: %v", dc.SessionMetadata, err)
		return
	}

	// add key-value pairs of metadata to context
	ctx = metadata.AppendToOutgoingContext(ctx, consts.GrpcSessionIDKey, s.id)
	ctx = metadata.AppendToOutgoingContext(ctx, consts.GrpcClientNameKey, c.deviceName)
	selectedProtocol, err := c.selectProtocol(sessionMetadata.Protocols)
	if err != nil {
		c.log.Errorf("failed to select protocol: %v", err)
	} else {
		// We expect that since this is missing an error will be sent to the client
		ctx = metadata.AppendToOutgoingContext(ctx, consts.GrpcSelectedProtocolKey, selectedProtocol)
	}
	streamClient, err := c.grpcClient.Stream(ctx)
	if err != nil {
		c.log.Errorf("error creating console stream client: %v", err)
		return
	}
	s.streamClient = streamClient
	s.run(ctx, sessionMetadata)
}

func (c *Manager) sync(ctx context.Context, desired *v1alpha1.DeviceSpec) {
	c.log.Debug("Syncing console status")
	defer c.log.Debug("Finished syncing console status")

	desiredConsoles := desired.GetConsoles()

	for _, d := range desiredConsoles {
		go c.start(ctx, d)
	}
}

func (c *Manager) Run(ctx context.Context) {
	c.log.Debug("Starting console controller")
	defer c.log.Debug("Stopping console controller")

	for {
		// The Pop() call will block until a new desired device spec is available
		// It is the responsibility of the publisher to stop the watcher
		// in case the controller needs to exit the loop.  When the publisher
		// stops, the watcher will be closed and the Pop() call will return
		// an error.
		desired, err := c.watcher.Pop()
		if err != nil {
			c.log.Warnf("failed to pop from spec watcher: %v", err)
			return
		}
		c.sync(ctx, desired.Spec)
	}
}

func setSize(fd uintptr, size v1alpha1.TerminalSize) error {
	winsize := &unix.Winsize{Row: size.Height, Col: size.Width}
	return unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, winsize)
}
