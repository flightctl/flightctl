package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/transport/remotecommand"
	"github.com/sirupsen/logrus"
)

type downloadStreamer struct {
	w                      http.ResponseWriter
	filename               string
	responseSent           bool
	inError                bool
	contentLengthExtracted bool
	stderr                 bytes.Buffer
	stdoutPrefix           bytes.Buffer
	contentLength          int64
	bytesSent              int64
	log                    logrus.FieldLogger
}

func (d *downloadStreamer) emitOk() {
	if !d.responseSent {
		d.responseSent = true
		d.w.Header().Set("Content-Type", "application/octet-stream")
		d.w.Header().Set("Content-Length", strconv.FormatInt(d.contentLength, 10))
		d.w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(d.filename)))
		d.w.WriteHeader(http.StatusOK)
	}
}

func (d *downloadStreamer) emitError() {
	if !d.responseSent {
		d.responseSent = true
		s := d.stderr.String()
		switch {
		case strings.Contains(s, "No such file or directory"):
			SetResponse(d.w, nil, api.StatusResourceNotFound("file", d.filename))
		case strings.Contains(s, "Permission denied"):
			SetResponse(d.w, nil, api.StatusUnauthorized("unauthorized to read file: "+d.filename))
		default:
			SetResponse(d.w, nil, api.StatusInternalServerError(fmt.Sprintf("failed downloading file %s: %s", d.filename, s)))
		}
	}
}

func (d *downloadStreamer) findNlIndex(b []byte) (int, error) {
	for i, c := range b {
		switch {
		case c == '\n':
			return i, nil
		case c >= '0' && c <= '9':
		default:
			return 0, fmt.Errorf("invalid NL index")
		}
	}
	return -1, nil
}

func (d *downloadStreamer) extractContentLength(bytes []byte) (contentLength int64, remaining []byte, err error) {
	var index int
	buffer := append(d.stdoutPrefix.Bytes(), bytes...)
	index, err = d.findNlIndex(buffer)
	if err != nil {
		return
	}
	if index == -1 {
		if len(buffer) > 40 {
			return 0, nil, fmt.Errorf("missing newline prefix")
		}
		d.stdoutPrefix.Write(bytes)
		return -1, nil, nil
	}
	contentLength, err = strconv.ParseInt(string(buffer[:index]), 10, 64)
	return contentLength, buffer[index+1:], err
}

func (d *downloadStreamer) OnStdout(bytes []byte) error {
	if !d.inError && !d.contentLengthExtracted {
		contentLength, remaining, err := d.extractContentLength(bytes)
		if contentLength == -1 || err != nil {
			return err
		}
		d.contentLengthExtracted = true
		d.contentLength = contentLength
		bytes = remaining
	}
	if !d.inError && len(bytes) > 0 {
		d.emitOk()
		n, err := d.w.Write(bytes)
		if err != nil {
			return err
		}
		d.bytesSent += int64(n)
	}
	return nil
}

func (d *downloadStreamer) OnStderr(bytes []byte) error {
	d.inError = true
	if d.stderr.Len() < 4096 {
		d.stderr.Write(bytes)
	}
	return nil
}

func (d *downloadStreamer) OnExit(code int) error {
	if d.inError == (code == 0) {
		d.log.Errorf("Got unexpected exit code: %d, but in-error = %v", code, d.inError)
	}
	if d.inError {
		d.emitError()
	} else {
		d.emitOk()
		if d.contentLength != d.bytesSent {
			d.log.Warnf("expected to send %d bytes, but sent %d bytes", d.contentLength, d.bytesSent)
		}
	}
	return nil
}

func (d *downloadStreamer) OnError(err error) error {
	d.log.Errorf("got error during download: %v", err)
	d.inError = true
	return nil
}

func (d *downloadStreamer) OnClose() error {
	return nil
}

func (h *TransportHandler) createDownloadMetadata(filename string, protocols []string) (string, error) {
	metadata := &api.DeviceConsoleSessionMetadata{
		Command: &api.DeviceCommand{
			Command: "stat",
			Args: []string{
				"-L",
				"-c",
				"%s",
				filename,
				"&&",
				"cat",
				filename,
			},
		},
		Protocols: protocols,
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (h *TransportHandler) downloadDeviceFile(w http.ResponseWriter, r *http.Request, name string, params api.DownloadDeviceFileParams) {
	orgId := store.NullOrgId
	if params.Filename == "" {
		SetResponse(w, nil, api.StatusBadRequest("filename is empty"))
		return
	}
	if _, status := h.serviceHandler.GetDevice(r.Context(), name); status.Code != http.StatusOK {
		SetResponse(w, nil, status)
		return
	}
	metadata, err := h.createDownloadMetadata(params.Filename, remotecommand.SupportedProtocols())
	if err != nil {
		SetResponse(w, nil, api.StatusInternalServerError("failed to create download metadata"))
		return
	}
	consoleSession, err := h.consoleSessionManager.StartSession(r.Context(), orgId, name, metadata)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceNotFound):
			SetResponse(w, nil, api.StatusResourceNotFound("device", name))
		default:
			SetResponse(w, nil, api.StatusInternalServerError("failed to create create console session"))
		}
		return
	}
	defer func() {
		_ = h.consoleSessionManager.CloseSession(r.Context(), consoleSession)
	}()
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	var (
		selectedProtocol string
		ok               bool
	)
	select {
	case selectedProtocol, ok = <-consoleSession.ProtocolCh:
		if !ok {
			SetResponse(w, nil, api.StatusInternalServerError(fmt.Sprintf("failed selecting protocol for device: %s", name)))
			return
		}
	case <-timer.C:
		SetResponse(w, nil, api.StatusInternalServerError(fmt.Sprintf("timed out waiting for protocol for device: %s", name)))
		return
	}
	streamer := &downloadStreamer{
		w:        w,
		filename: params.Filename,
		log:      h.log,
	}
	remoteSession, err := remotecommand.NewSession(selectedProtocol, consoleSession.RecvCh, consoleSession.SendCh, streamer, h.log)
	if err != nil {
		SetResponse(w, nil, api.StatusInternalServerError(fmt.Sprintf("failed selecting protocol for device: %s", name)))
		return
	}
	defer func() {
		_ = remoteSession.Close()
	}()
	_ = remoteSession.HandleIncoming()
}
