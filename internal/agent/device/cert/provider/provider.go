package provider

import (
	"context"
	"io"
)

type StorageWriter interface {
	Write(certPEM, keyPEM []byte) error
	WriteFrom(cert io.Reader, key io.Reader) error
}

type Storage interface {
	Load(ctx context.Context) (certPEM, keyPEM []byte, err error)
	Writer(ctx context.Context) (StorageWriter, error)
	Delete(ctx context.Context) error
}

type Provisioner interface {
	Provision(ctx context.Context) error               // starts the async process
	Result(ctx context.Context, w StorageWriter) error // blocks until cert is ready, then writes via the writer
}
