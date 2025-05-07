//go:build amd64 || arm64

package tpm

import (
	tpmSimulator "github.com/google/go-tpm-tools/simulator"
)

func OpenTPMSimulator() (*TPM, error) {
	simulator, err := tpmSimulator.Get()
	if err != nil {
		return &TPM{}, err
	}
	return &TPM{channel: simulator}, nil
}
