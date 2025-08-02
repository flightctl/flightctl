package healthcheck

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	Run(ctx context.Context, wg *sync.WaitGroup)
	SetClient(client.Management)
}

type manager struct {
	deviceName       string
	managementClient client.Management
	interval         time.Duration
	log              *log.PrefixLogger
}

func New(
	deviceName string,
	interval time.Duration,
	log *log.PrefixLogger,
) Manager {
	return &manager{
		deviceName: deviceName,
		interval:   interval,
		log:        log,
	}
}

func (m *manager) Run(ctx context.Context, wg *sync.WaitGroup) {
	m.log.Infof("Starting healthcheck manager for device %s with interval %s", m.deviceName, m.interval)
	defer m.log.Infof("Stopping healthcheck manager for device %s", m.deviceName)
	defer wg.Done()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.update(ctx)
		}
	}
}

func (m *manager) update(ctx context.Context) {
	if err := m.managementClient.HealthcheckDevice(ctx, m.deviceName); err != nil {
		m.log.Warnf("Failed to healthcheck device status: %v", err)
	}
}

func (m *manager) SetClient(client client.Management) {
	m.managementClient = client
}
