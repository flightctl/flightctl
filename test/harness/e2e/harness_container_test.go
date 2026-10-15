package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/onsi/gomega"
)

type cleanupTestDevice struct {
	vm.TestVMInterface
	harness *Harness
	test    *testing.T
	deleted bool
}

func (device *cleanupTestDevice) IsRunning() (bool, error) {
	if device.harness.VM != device {
		device.test.Fatal("device was detached before diagnostics could check it")
	}
	return false, nil
}

func (device *cleanupTestDevice) ForceDelete() error {
	if device.harness.VM != device {
		device.test.Fatal("device was detached before cleanup")
	}
	device.deleted = true
	return nil
}

func TestCleanupContainerFromPool(t *testing.T) {
	gomega.RegisterTestingT(t)
	for _, hasDevice := range []bool{false, true} {
		t.Run("When cleaning up a harness it should retain its device until cleanup finishes", func(t *testing.T) {
			_, cancel := context.WithCancel(context.Background())
			harness := &Harness{ctxCancel: cancel, startTime: time.Now()}
			device := &cleanupTestDevice{harness: harness, test: t}
			if hasDevice {
				harness.VM = device
			}
			CleanupContainerFromPool(harness, 1)
			if harness.VM != nil || device.deleted != hasDevice {
				t.Fatalf("device = %v, deleted = %v, want nil, %v", harness.VM, device.deleted, hasDevice)
			}
		})
	}
}
