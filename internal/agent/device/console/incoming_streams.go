package console

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

type incomingStream interface {
	handle([]byte) error
	close()
}

type incomingStreams struct {
	streamClient grpc_v1.RouterService_StreamClient
	streams      map[byte]incomingStream
	log          *log.PrefixLogger
}

func newIncomingStreams(streamClient grpc_v1.RouterService_StreamClient, stdin io.WriteCloser, resizeFd uintptr, closers map[byte]io.Closer, log *log.PrefixLogger) *incomingStreams {
	ret := &incomingStreams{
		streamClient: streamClient,
		streams:      make(map[byte]incomingStream),
		log:          log,
	}
	ret.streams[StdinID] = newStdinStream(stdin, log)
	if resizeFd != 0 {
		ret.streams[ResizeID] = newResizeStream(resizeFd, log)
	}
	ret.streams[CloseID] = newCloseStream(closers, log)
	return ret
}

func (i *incomingStreams) run(wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() {
		for _, s := range i.streams {
			s.close()
		}
		i.log.Debug("incoming streams finished")
	}()
	i.log.Debug("starting incoming streams")
	for {
		msg, err := i.streamClient.Recv()
		if err == io.EOF || msg != nil && msg.Closed {
			i.log.Debug("stream > bash: connection closed")
			return
		}
		if err != nil {
			i.log.Errorf("stream > bash:error receiving message for stdin: %s", err)
			return
		}
		payload := msg.GetPayload()
		i.log.Debugf("stream > bash: received: %s", (string)(payload))

		if len(payload) == 0 {
			i.log.Error("empty incoming payload")
			return
		}
		streamId := payload[0]
		stream := i.streams[streamId]
		if stream == nil {
			i.log.Errorf("unexpected stream is %v", streamId)
			return
		}
		if err = stream.handle(payload[1:]); err != nil {
			i.log.Errorf("failed handling stream: %v", err)
			return
		}
	}
}

func (i *incomingStreams) start(wg *sync.WaitGroup) {
	wg.Add(1)
	go i.run(wg)
}

type stdinStream struct {
	stdin io.WriteCloser
	log   *log.PrefixLogger
}

func newStdinStream(stdin io.WriteCloser, log *log.PrefixLogger) incomingStream {
	return &stdinStream{
		stdin: stdin,
		log:   log,
	}
}

func (s *stdinStream) handle(msg []byte) error {
	_, err := s.stdin.Write(msg)
	if err != nil {
		s.log.Errorf("write to stdin: %v", err)
	}
	return err
}

func (s *stdinStream) close() {
	_ = s.stdin.Close()
}

type resizeStream struct {
	resizeFd uintptr
	log      *log.PrefixLogger
}

func newResizeStream(resizeFd uintptr, log *log.PrefixLogger) incomingStream {
	return &resizeStream{
		resizeFd: resizeFd,
		log:      log,
	}
}

func (r *resizeStream) handle(msg []byte) error {
	var size v1alpha1.TerminalSize
	err := json.Unmarshal(msg, &size)
	if err != nil {
		r.log.Errorf("failed to unmarshal resize message: %v", err)
		return err
	}
	return setSize(r.resizeFd, size)
}

func (r *resizeStream) close() {
}

type closeStream struct {
	closers map[byte]io.Closer
	log     *log.PrefixLogger
}

func newCloseStream(closers map[byte]io.Closer, log *log.PrefixLogger) incomingStream {
	return &closeStream{
		closers: closers,
		log:     log,
	}
}

func (c *closeStream) handle(msg []byte) error {
	if len(msg) != 1 {
		return fmt.Errorf("close: expected exactly single byte message")
	}
	c.log.Debugf("closing stream %d", int(msg[0]))
	closer, exists := c.closers[msg[0]]
	if !exists {
		return fmt.Errorf("close: closer with id %v doesn't exist", msg[0])
	}
	_ = closer.Close()
	return nil
}

func (c *closeStream) close() {}
