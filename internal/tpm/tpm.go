package tpm

import (
	"encoding/hex"
	"fmt"
	"io"

	"github.com/google/go-tpm-tools/simulator"
	"github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

type TPM struct {
	devicePath string
	channel    io.ReadWriteCloser
}

func OpenTPM(devicePath string) (*TPM, error) {
	ch, err := tpmutil.OpenTPM(devicePath)
	if err != nil {
		return nil, err
	}
	return &TPM{devicePath: devicePath, channel: ch}, nil
}

func OpenTPMSimulator() (*TPM, error) {
	simulator, err := simulator.Get()
	if err != nil {
		return &TPM{}, err
	}
	return &TPM{channel: simulator}, nil
}

func (t *TPM) Close() {
	if t != nil {
		t.channel.Close()
	}
}

func (t *TPM) GetPCRValues(measurements map[string]string) error {
	if t == nil {
		return nil
	}
	for pcr := 1; pcr <= 16; pcr++ {
		key := fmt.Sprintf("pcr%02d", pcr)
		val, err := tpm2.ReadPCR(t.channel, pcr, tpm2.AlgSHA256)
		if err != nil {
			return err
		}
		measurements[key] = hex.EncodeToString(val)
	}
	return nil
}
