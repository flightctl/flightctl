//go:build !(amd64 || arm64)

package tpm

import "errors"

func OpenTPMSimulator() (*TPM, error) {
	return &TPM{}, errors.New("TPM simulator not supported")
}
