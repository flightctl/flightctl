package agent

import (
	"fmt"
	"sync"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/cloudevents/util"
	"github.com/flightctl/flightctl/pkg/log"
)

type deviceCache struct {
	sync.RWMutex

	log    *log.PrefixLogger
	device *v1alpha1.Device // latest device

	deviceName string
}

func NewDeviceCache(log *log.PrefixLogger, deviceName string) *deviceCache {
	return &deviceCache{
		log:        log,
		deviceName: deviceName,
	}
}

func (s *deviceCache) Upsert(device *v1alpha1.Device) error {
	s.Lock()
	defer s.Unlock()

	if *device.Metadata.Name != s.deviceName {
		return fmt.Errorf("unexpected device %s (%s)", *device.Metadata.Name, s.deviceName)
	}

	// compare version
	if len(device.Version()) == 0 {
		return fmt.Errorf("no version is provided for the received device %s", s.deviceName)
	}

	if s.device != nil && !util.CompareDeviceVersion(s.device.Version(), device.Version()) {
		s.log.Infof("ignore the device %s (%s > %s)",
			s.deviceName, s.device.Version(), device.Version())
		return nil
	}

	s.device = device
	return nil
}

func (s *deviceCache) UpdateStatus(device v1alpha1.Device) {
	s.Lock()
	defer s.Unlock()

	if s.device.Version() != device.Version() {
		s.log.Infof("ignore the device %s (%s != %s)",
			s.deviceName, s.device.Version(), device.Version())
		return
	}

	s.device.Status = device.Status
}

func (s *deviceCache) Get(name string) (*v1alpha1.Device, bool) {
	s.RLock()
	defer s.RUnlock()

	if s.device == nil {
		return s.device, false
	}

	return s.device, true
}

func (s *deviceCache) List() []*v1alpha1.Device {
	s.RLock()
	defer s.RUnlock()

	if s.device == nil {
		return []*v1alpha1.Device{}
	}

	return []*v1alpha1.Device{s.device}
}
