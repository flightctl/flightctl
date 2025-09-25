package tpm

import (
	"crypto"
	"fmt"
	"io"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

type exportableDeviceID struct {
	log          *log.PrefixLogger
	parentHandle tpm2.TPMHandle
	pub          tpm2.TPM2BPublic
	priv         tpm2.TPM2BPrivate
	loadedHandle tpm2.AuthHandle
	conn         io.ReadWriteCloser
}

func (e *exportableDeviceID) Public() crypto.PublicKey {
	pub, err := convertTPM2BPublicToPublicKey(&e.pub)
	if err != nil {
		e.log.Errorf("Failed to convert tpm blob to public key: %v", err)
		return nil
	}
	return pub
}

func (e *exportableDeviceID) PublicBlob() []byte {
	return tpm2.Marshal(e.pub)
}

func (e *exportableDeviceID) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return signWithKey(transport.FromReadWriter(e.conn), e.Handle(), digest)
}

func (e *exportableDeviceID) Close() error {
	flushCmd := tpm2.FlushContext{FlushHandle: e.loadedHandle.Handle}
	_, err := flushCmd.Execute(transport.FromReadWriter(e.conn))
	if err != nil {
		return fmt.Errorf("flushing device ID: %w", err)
	}
	return nil
}

func (e *exportableDeviceID) Handle() tpm2.AuthHandle {
	return e.loadedHandle
}

func (e *exportableDeviceID) Export() ([]byte, error) {
	return GenerateTPM2KeyFile(LoadableKey, e.parentHandle, e.pub, e.priv)
}
