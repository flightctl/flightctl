package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"

	kcert "k8s.io/client-go/util/cert"

	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/middleware"
)

var (
	ErrEmptyResponse = errors.New("empty response")
)

func NewWithResponses(endpoint string, caFilePath, certFilePath, keyFilePath string) (*client.ClientWithResponses, error) {
	url, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := newTlsConfig(caFilePath, certFilePath, keyFilePath)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.GetReqID())
		return nil
	})

	return client.NewClientWithResponses(url.String(), client.WithHTTPClient(httpClient), ref)
}

func newTlsConfig(caFilePath, certFilePath, keyFilePath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFilePath, keyFilePath)
	if err != nil {
		return nil, err
	}
	caCertPool, err := kcert.NewPool(caFilePath)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}
