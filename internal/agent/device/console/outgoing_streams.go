package console

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/remotecommand"
)

type outgoingStreams struct {
	streamClient grpc_v1.RouterService_StreamClient
	input        chan []byte
	log          *log.PrefixLogger
	streams      []outgoingStream
}

func newOutgoingStreams(streamClient grpc_v1.RouterService_StreamClient, cmd *exec.Cmd, stdout, stderr io.ReadCloser, log *log.PrefixLogger) *outgoingStreams {
	ret := &outgoingStreams{
		streamClient: streamClient,
		input:        make(chan []byte, 2),
		log:          log,
	}
	ret.streams = append(ret.streams, newOutgoingStdStream(ret.input, stdout, StdoutID, log))
	if stderr != nil {
		ret.streams = append(ret.streams, newOutgoingStdStream(ret.input, stderr, StderrID, log))
	}
	ret.streams = append(ret.streams, newErrorStream(ret.input, cmd, log))
	return ret
}

func (o *outgoingStreams) run(wg *sync.WaitGroup) {
	var sent, packets int
	defer wg.Done()
	defer func() {
		o.log.Debugf("closing outgoing streams.  sent packets %d, sent bytes %d", packets, sent)
		for _, s := range o.streams {
			s.close()
		}
		// Closing this gRPC stream causes the incoming streams goroutine to terminate.  It also causes the client
		// session to be terminated
		_ = o.streamClient.CloseSend()
	}()
	o.log.Debug("starting outgoing streams")
	for msg := range o.input {
		packets++
		sent += len(msg)
		err := o.streamClient.Send(&grpc_v1.StreamRequest{
			Payload: msg,
		})
		if err != nil {
			o.log.Errorf("failed sending outgoing message: %v", err)
			return
		}
	}
}

func (o *outgoingStreams) start(wg *sync.WaitGroup) {
	wg.Add(1)
	go o.run(wg)
	var childWg sync.WaitGroup
	for _, s := range o.streams {
		childWg.Add(1)
		wg.Add(1)
		go s.run(wg, &childWg)
	}
}

type outgoingStream interface {
	run(parentWg, childWg *sync.WaitGroup)
	close()
}

type outgoingStreamBase struct {
	id     byte
	output chan []byte
}

func (o *outgoingStreamBase) send(payload []byte) {
	o.output <- append([]byte{o.id}, payload...)
}

type outgoingStdStream struct {
	outgoingStreamBase
	stdStream io.ReadCloser
	log       *log.PrefixLogger
}

func newOutgoingStdStream(output chan []byte, stdStream io.ReadCloser, id byte, log *log.PrefixLogger) outgoingStream {
	return &outgoingStdStream{
		outgoingStreamBase: outgoingStreamBase{
			id:     id,
			output: output,
		},
		stdStream: stdStream,
		log:       log,
	}
}

func (o *outgoingStdStream) run(parentWg, childWg *sync.WaitGroup) {
	var sent, packets int
	defer parentWg.Done()
	defer childWg.Done()
	defer o.close()

	defer func() {
		o.log.Debugf("finished std stream.  sent %d bytes, %d packets", sent, packets)
	}()
	buffer := make([]byte, 4096)
	for {
		n, err := o.stdStream.Read(buffer)
		if n > 0 {
			sent += n
			packets++
			o.send(buffer[:n])
		}
		if err != nil {
			if err != io.EOF {
				o.log.Errorf("sending data: %v", err)
			}
			if n == 0 {
				return
			}
		}
	}
}

func (o *outgoingStdStream) close() {
	_ = o.stdStream.Close()
}

type outgoingErrorStream struct {
	outgoingStreamBase
	cmd *exec.Cmd
	log *log.PrefixLogger
}

func newErrorStream(output chan []byte, cmd *exec.Cmd, log *log.PrefixLogger) outgoingStream {
	return &outgoingErrorStream{
		outgoingStreamBase: outgoingStreamBase{
			id:     ErrID,
			output: output,
		},
		cmd: cmd,
		log: log,
	}
}

func (o *outgoingErrorStream) emit(err error) {
	o.log.Debugf("emitting error %v", err)

	var exitCode int
	switch actual := err.(type) {
	case nil:
	case *exec.ExitError:
		exitCode = actual.ExitCode()
	default:
		o.log.Errorf("unexpected error type %T: %v", err, err)
		exitCode = 555
	}
	status := metav1.Status{
		Status: lo.Ternary[string](exitCode == 0, metav1.StatusSuccess, metav1.StatusFailure),
		Code:   int32(exitCode),
	}
	if exitCode != 0 {
		status.Reason = remotecommand.NonZeroExitCodeReason
		status.Details = &metav1.StatusDetails{
			Causes: []metav1.StatusCause{
				{
					Type:    remotecommand.ExitCodeCauseType,
					Message: fmt.Sprintf("%d", exitCode),
				},
			},
		}
	}
	b, err := json.Marshal(&status)
	if err != nil {
		o.log.Errorf("failed to marshal status: %v", err)
		return
	}
	o.send(b)
}

func (o *outgoingErrorStream) run(parentWg, childWg *sync.WaitGroup) {
	defer parentWg.Done()

	// Closing the channel will cause the parent outgoing streams to terminate
	defer close(o.output)

	// Since this stream waits for other streams to finish, then this Done() function
	// needs to be called to avoid counting the current stream
	childWg.Done()

	// Wait for all other streams to finish reading.  This must be called before calling
	// Wait() on the process.  Please see https://pkg.go.dev/os/exec#Cmd.StdoutPipe for more details.
	childWg.Wait()

	// Wait for the process to finish and emit the result
	o.emit(o.cmd.Wait())
}

func (o *outgoingErrorStream) close() {}
