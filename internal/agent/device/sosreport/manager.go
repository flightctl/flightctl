package sosreport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"regexp"

	"github.com/flightctl/flightctl/api/v1alpha1"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
	"github.com/flightctl/flightctl/internal/service/sosreport"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/json"
)

type Manager interface {
	Sync(ctx context.Context, current, desired *v1alpha1.Device) error
	SetClient(client.ClientWithResponsesInterface)
}

func NewManager(log *log.PrefixLogger) Manager {
	return &manager{
		log: log,
	}
}

type manager struct {
	client client.ClientWithResponsesInterface
	log    *log.PrefixLogger
}

var _ Manager = (*manager)(nil)

func (m *manager) fileReader(name, fileName string) (reader io.ReadCloser, contentType string, err error) {
	var fileReader *os.File
	fileReader, err = os.Open(fileName)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			fileReader.Close()
		}
	}()
	r, w := io.Pipe()
	writer := multipart.NewWriter(w)
	go func() {
		defer func() {
			writer.Close()
			w.Close()
			fileReader.Close()
		}()
		partWriter, err := writer.CreateFormFile(name, fileName)
		if err == nil {
			io.Copy(partWriter, fileReader)
		}
	}()
	return r, writer.FormDataContentType(), nil
}

func (m *manager) jsonReader(name string, content []byte) (reader io.ReadCloser, contentType string, err error) {
	r, w := io.Pipe()
	writer := multipart.NewWriter(w)
	go func() {
		defer func() {
			writer.Close()
			w.Close()
		}()
		jsonHeader := make(textproto.MIMEHeader)
		jsonHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, name))
		jsonHeader.Set("Content-Type", "application/json")
		partWriter, err := writer.CreatePart(jsonHeader)
		if err == nil {
			io.Copy(partWriter, bytes.NewBuffer(content))
		}
	}()
	return r, writer.FormDataContentType(), nil
}

func (m *manager) extractIdsFromAnnotations(annotations map[string]string) ([]string, error) {
	annotationStr, ok := util.GetFromMap(annotations, v1alpha1.DeviceAnnotationSosReports)
	if !ok {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(annotationStr), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (m *manager) extractReportFileName(stdoutStr string) (string, error) {
	r := regexp.MustCompile(`Your sos report has been generated and saved in:\s+(\S+)\s`)
	matches := r.FindStringSubmatch(stdoutStr)
	if len(matches) != 2 {
		return "", fmt.Errorf("couldn't extract output file")
	}
	return matches[1], nil
}

func (m *manager) emitPart(ctx context.Context, id, contentType string, reader io.ReadCloser) {
	defer reader.Close()
	sessionID, err := uuid.Parse(id)
	if err != nil {
		m.log.Errorf("failed to parse uuid %s: %v", id, err)
		return
	}
	response, err := m.client.UploadSosReportWithBodyWithResponse(ctx, sessionID, contentType, reader)
	switch response.StatusCode() {
	case http.StatusNoContent:
	default:
		m.log.Errorf("unexpected status code %d received for sos report upload", response.StatusCode())
	}
}

func (m *manager) emitError(ctx context.Context, id string, err error) {
	m.log.Errorf("Emitting error: %v", err)
	status := v1alpha1.Status{
		Code:    http.StatusInternalServerError,
		Kind:    "SosReport",
		Message: fmt.Sprintf("Agent error: %v", err),
		Reason:  "UploadFailure",
		Status:  "Failure",
	}
	b, err := json.Marshal(&status)
	if err != nil {
		m.log.Errorf("failed to marshal status: %v", err)
		return
	}
	reader, contentType, err := m.jsonReader(sosreport.ErrorFormName, b)
	if err != nil {
		m.log.Errorf("failed to create error part: %v", err)
		return
	}
	m.emitPart(ctx, id, contentType, reader)
}

func (m *manager) emitReport(ctx context.Context, id, filename string) {
	reader, contentType, err := m.fileReader(sosreport.ReportFormName, filename)
	if err != nil {
		m.emitError(ctx, id, fmt.Errorf("failed to create multipart file reader: %w", err))
		return
	}
	_ = os.Remove(filename)
	m.emitPart(ctx, id, contentType, reader)
}

func (m *manager) run(ctx context.Context, id string) {
	cmd := exec.CommandContext(ctx, "sos", "report", "--batch", "--quiet", "--case-id", id)
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		m.emitError(ctx, id, fmt.Errorf("failed to run sos report: %w", err))
		return
	}
	filename, err := m.extractReportFileName(stdout.String())
	if err != nil {
		m.emitError(ctx, id, fmt.Errorf("failed to extract output file from output: %w", err))
		return
	}
	m.emitReport(ctx, id, filename)
}

func (m *manager) Sync(ctx context.Context, current, desired *v1alpha1.Device) error {
	m.log.Info("sosreport sync")
	currentAnnotations := lo.FromPtr(current.Metadata.Annotations)
	desiredAnnotations := lo.FromPtr(desired.Metadata.Annotations)
	currentIds, err := m.extractIdsFromAnnotations(currentAnnotations)
	if err != nil {
		return err
	}
	desiredIds, err := m.extractIdsFromAnnotations(desiredAnnotations)
	if err != nil {
		return err
	}
	m.log.Infof("sosreport current %+v desired %+v", currentIds, desiredIds)
	idsToLaunch := lo.Without(desiredIds, currentIds...)
	for _, id := range idsToLaunch {
		m.log.Infof("sosreport launching %s", id)
		go m.run(ctx, id)
	}
	return nil
}

func (m *manager) SetClient(client client.ClientWithResponsesInterface) {
	m.client = client
}
