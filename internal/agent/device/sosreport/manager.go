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
	"sync"

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
	GenerateAndUpdate(ctx context.Context, id uuid.UUID) error
	SetClient(client.ClientWithResponsesInterface)
}

func NewManager(log *log.PrefixLogger) Manager {
	return &manager{
		log: log,
	}
}

type manager struct {
	mu               sync.Mutex
	inProgress       []uuid.UUID
	frozenInProgress []uuid.UUID
	client           client.ClientWithResponsesInterface
	log              *log.PrefixLogger
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
			if _, err := io.Copy(partWriter, fileReader); err != nil {
				m.log.WithError(err).Error("failed to copy file")
			}
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
			if _, err := io.Copy(partWriter, bytes.NewBuffer(content)); err != nil {
				m.log.WithError(err).Error("failed to copy JSON content")
			}
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

func (m *manager) emitPart(ctx context.Context, sessionID uuid.UUID, contentType string, reader io.ReadCloser) error {
	defer reader.Close()
	response, err := m.client.UploadSosReportWithBodyWithResponse(ctx, sessionID, contentType, reader)
	switch response.StatusCode() {
	case http.StatusNoContent:
		return nil
	default:
		err = fmt.Errorf("unexpected status code %d received for sos report upload", response.StatusCode())
		m.log.Errorf("emitPart: %v %v", err, string(response.Body))
		return err
	}
}

func (m *manager) emitError(ctx context.Context, id uuid.UUID, err error) error {
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
		return err
	}
	reader, contentType, err := m.jsonReader(sosreport.ErrorFormName, b)
	if err != nil {
		m.log.Errorf("failed to create error part: %v", err)
		return err
	}
	return m.emitPart(ctx, id, contentType, reader)
}

func (m *manager) emitReport(ctx context.Context, id uuid.UUID, filename string) error {
	reader, contentType, err := m.fileReader(sosreport.ReportFormName, filename)
	if err != nil {
		return m.emitError(ctx, id, fmt.Errorf("failed to create multipart file reader: %w", err))
	}
	return m.emitPart(ctx, id, contentType, reader)
}

func (m *manager) addId(id uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lo.Contains(m.frozenInProgress, id) {
		return true
	}
	skip := len(m.inProgress) != 0
	m.inProgress = lo.Uniq(append(m.inProgress, id))
	return skip
}

func (m *manager) freezeIds() []uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frozenInProgress = m.inProgress
	m.inProgress = nil
	return m.frozenInProgress
}

func (m *manager) emitResult(ctx context.Context, id uuid.UUID, filename string, runErr error, wg *sync.WaitGroup) {
	var err error
	defer func() {
		if err != nil {
			m.log.WithError(err).Error("emitResult")
		}
	}()
	defer wg.Done()
	if runErr != nil {
		err = m.emitError(ctx, id, fmt.Errorf("failed to run sos report: %w", runErr))
	} else {
		err = m.emitReport(ctx, id, filename)
	}
}

func (m *manager) emitAll(ctx context.Context, filename string, runErr error) {
	frozenIds := m.freezeIds()
	var wg sync.WaitGroup
	for _, id := range frozenIds {
		wg.Add(1)
		go m.emitResult(ctx, id, filename, runErr, &wg)
	}
	wg.Wait()
}

func (m *manager) GenerateAndUpdate(ctx context.Context, id uuid.UUID) error {
	m.log.Info("sosreport GenerateAndUpdate")
	if m.addId(id) {
		return nil
	}
	cmd := exec.CommandContext(ctx, "sos", "report", "--batch", "--quiet")
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	var filename string
	if err == nil {
		filename, err = m.extractReportFileName(stdout.String())
		if err == nil {
			defer os.Remove(filename)
		}
	}
	m.emitAll(ctx, filename, err)
	return nil
}

func (m *manager) SetClient(client client.ClientWithResponsesInterface) {
	m.client = client
}
