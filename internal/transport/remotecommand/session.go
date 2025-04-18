package remotecommand

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/remotecommand"
)

var protocols = []string{
	api.StreamProtocolV5Name,
}

type SessionCallbacks interface {
	OnStdout([]byte) error
	OnStderr([]byte) error
	OnExit(code int) error
	OnError(err error) error
	OnClose() error
}

type Session interface {
	io.Closer
	HandleIncoming() error
	SendStdinStream(data []byte) error
	SendResizeStream(width, height uint16) error
	SendCloseStdinStream() error
}

type session struct {
	callbacks  SessionCallbacks
	incomingCh chan []byte
	outgoingCh chan []byte
	mu         sync.Mutex
	closed     bool
	log        logrus.FieldLogger
}

func SupportedProtocols() []string {
	return protocols
}

func NewSession(selectedProtocol string, incomingCh, outgoingCh chan []byte, callbacks SessionCallbacks, log logrus.FieldLogger) (Session, error) {
	if !slices.Contains(protocols, selectedProtocol) {
		return nil, fmt.Errorf("unsupported protocol: %s", selectedProtocol)
	}
	return &session{
		callbacks:  callbacks,
		incomingCh: incomingCh,
		outgoingCh: outgoingCh,
		log:        log,
	}, nil
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.outgoingCh)
	}
	return nil
}

func (s *session) sendExitCode(code int) error {
	if s.callbacks != nil {
		return s.callbacks.OnExit(code)
	}
	return nil
}

func (s *session) emitStdoutStream(message []byte) error {
	if s.callbacks != nil {
		return s.callbacks.OnStdout(message)
	}
	return nil
}

func (s *session) emitStderrStream(message []byte) error {
	if s.callbacks != nil {
		return s.callbacks.OnStderr(message)
	}
	return nil
}

func (s *session) emitErrorStream(message []byte) error {
	status := metav1.Status{}
	err := json.Unmarshal(message, &status)
	if err != nil {
		s.log.Errorf("failed to unmarshal status message: %v", err)
		return s.sendExitCode(-1)
	}
	switch status.Status {
	case metav1.StatusSuccess:
		return s.sendExitCode(0)
	case metav1.StatusFailure:
		if status.Reason == remotecommand.NonZeroExitCodeReason {
			if status.Details == nil {
				s.log.Errorf("error stream protocol error: details must be set")
				return s.sendExitCode(-1)
			}
			for i := range status.Details.Causes {
				c := &status.Details.Causes[i]
				if c.Type != remotecommand.ExitCodeCauseType {
					continue
				}

				rc, err := strconv.ParseInt(c.Message, 10, 8)
				if err != nil {
					s.log.Errorf("error stream protocol error: invalid exit code value %q", c.Message)
					return s.sendExitCode(-1)
				}
				s.log.Errorf("command terminated with exit code %d", rc)
				return s.sendExitCode(int(rc))
			}

			s.log.Errorf("error stream protocol error: no %s cause given", remotecommand.ExitCodeCauseType)
			return s.sendExitCode(-1)
		}
	default:
		s.log.Error("error stream protocol error: unknown error")
		return s.sendExitCode(-1)
	}

	s.log.Error(status.Message)
	return s.sendExitCode(-1)
}

func (s *session) emitClose() {
	if s.callbacks != nil {
		if err := s.callbacks.OnClose(); err != nil {
			s.log.Errorf("emitClose: %v", err)
		}
	}
}

func (s *session) handlePayload(payload []byte) error {
	if len(payload) == 0 {
		s.log.Errorf("handlePayload: empty payload")
		return fmt.Errorf("handlePayload: empty payload")
	}
	switch payload[0] {
	case api.StdoutStreamID:
		return s.emitStdoutStream(payload[1:])
	case api.StderrStreamID:
		return s.emitStderrStream(payload[1:])
	case api.ErrStreamID:
		return s.emitErrorStream(payload[1:])
	default:
		s.log.Errorf("unexpected stream id: %d", int(payload[0]))
		return fmt.Errorf("unexpected stream id: %d", int(payload[0]))
	}
}

func (s *session) HandleIncoming() error {
	defer func() {
		s.emitClose()
		_ = s.Close()
	}()
	for msg := range s.incomingCh {
		if err := s.handlePayload(msg); err != nil {
			s.log.Errorf("failed to handle payload: %v", err)
			return err
		}
	}
	return nil
}

func (s *session) sendInputStream(message []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.outgoingCh <- message
		return nil
	}
	return fmt.Errorf("outgoingCh ar already closed")
}

func (s *session) SendStdinStream(data []byte) error {
	return s.sendInputStream(append([]byte{api.StdinStreamID}, data...))
}

func (s *session) SendResizeStream(width, height uint16) error {
	t := api.TerminalSize{
		Width:  width,
		Height: height,
	}
	b, err := json.Marshal(&t)
	if err != nil {
		return err
	}
	return s.sendInputStream(append([]byte{api.ResizeStreamID}, b...))
}

func (s *session) SendCloseStdinStream() error {
	return s.sendInputStream([]byte{api.CloseStreamID, api.StdinStreamID})
}
