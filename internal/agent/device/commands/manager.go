package commands

import (
	"context"
	"fmt"
	"net/http"

	agent "github.com/flightctl/flightctl/api/v1alpha1/agent"
	"github.com/flightctl/flightctl/internal/agent/device/sosreport"
	client "github.com/flightctl/flightctl/internal/api/client/agent"
	"github.com/samber/lo"

	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	NextCommands(ctx context.Context)
	SetClient(client.ClientWithResponsesInterface)
}

type manager struct {
	deviceName string
	sosreports sosreport.Manager
	client     client.ClientWithResponsesInterface
	log        *log.PrefixLogger
}

func NewManager(deviceName string,
	sosreports sosreport.Manager,
	log *log.PrefixLogger) Manager {
	return &manager{
		deviceName: deviceName,
		sosreports: sosreports,
		log:        log,
	}
}

var _ Manager = (*manager)(nil)

func (m *manager) processCommand(ctx context.Context, cmd agent.DeviceCommand) {
	value, err := cmd.ValueByDiscriminator()
	if err != nil {
		m.log.WithError(err).Error("failed to run ValueByDiscriminator")
		return
	}
	switch actual := value.(type) {
	case agent.UploadSosReportCommand:
		err = m.sosreports.GenerateAndUpdate(ctx, actual.Id)
	default:
		err = fmt.Errorf("unexpected type %T from ValueByDiscriminator", value)
	}
	if err != nil {
		m.log.WithError(err).Error("processCommand")
	}
}

func (m *manager) NextCommands(ctx context.Context) {
	response, err := m.client.GetNextCommandsWithResponse(ctx, m.deviceName)
	if err != nil {
		m.log.WithError(err).Error("GetNextCommandsWithResponse")
		return
	}
	switch response.StatusCode() {
	case http.StatusOK:
		break
	case http.StatusNotFound:
		m.log.Errorf("Not found: %+v %s %s", lo.FromPtr(response.JSON404), response.Status(), string(response.Body))
	default:
		m.log.Errorf("unexpected status code %d", response.StatusCode())
		return
	}
	commands := lo.FromPtr(lo.FromPtr(response.JSON200).Commands)
	for i := range commands {
		cmd := commands[i]
		go m.processCommand(ctx, cmd)
	}
}

func (m *manager) SetClient(client client.ClientWithResponsesInterface) {
	m.client = client
	m.sosreports.SetClient(client)
}
