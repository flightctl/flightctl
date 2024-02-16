package device

import (
	"crypto"
	"encoding/hex"
	"sync"

	v1alpha "github.com/flightctl/flightctl/api/v1alpha1"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
)

// New returns a new device.
func New(
	publicKey crypto.PublicKey,
	privateKey crypto.PrivateKey,
) (*Device, error) {
	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	name := hex.EncodeToString(publicKeyHash)
	csr, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), name)
	if err != nil {
		return nil, err
	}
	return &Device{
		device: v1alpha.Device{
			ApiVersion: "v1alpha1",
			Kind:       "Device",
			Status:     &v1alpha.DeviceStatus{},
			Metadata: v1alpha.ObjectMeta{
				Name: &name,
			},
		},
		csr: csr,
	}, nil
}

type Device struct {
	// mutex to protect the device resource
	mu sync.RWMutex
	// The device resource manifest
	device v1alpha.Device
	// The device's enrollment CSR
	csr []byte
}

// Set updates the local device resource.
func (d *Device) Set(r v1alpha.Device) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.device = r
}

// Get returns a reference to the device resource.
func (d *Device) Get() *v1alpha.Device {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return &d.device
}

func (d *Device) Fingerprint() *string {
	return d.Get().Metadata.Name
}
