package bearertokenauthextension

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/extensionauth"
	"github.com/sirupsen/logrus"
)

var (
	_ catalogcollector.Extension = (*extension)(nil)
	_ extensionauth.HTTPClient   = (*extension)(nil)
)

type extension struct {
	log       *logrus.Entry
	token     api.SecureString
	tokenFile string
}

func (e *extension) Start(
	_ context.Context,
	_ catalogcollector.Host,
) error {
	if e.tokenFile != "" {
		e.log.WithField("token_source", "file").Info("extension started")
	} else {
		e.log.WithField("token_source", "inline").Info("extension started")
	}
	return nil
}

func (e *extension) Shutdown(_ context.Context) error {
	e.log.Info("extension stopped")
	return nil
}

// RoundTripper returns an HTTP transport that adds bearer authentication to
// every outgoing request.
//
// When tokenFile is configured, the file is read for every request so credential
// rotation takes effect without restarting the collector.
func (e *extension) RoundTripper(
	base http.RoundTripper,
) (http.RoundTripper, error) {
	if base == nil {
		base = http.DefaultTransport
	}

	return &tokenRoundTripper{
		token:     e.token,
		tokenFile: e.tokenFile,
		base:      base,
	}, nil
}

type tokenRoundTripper struct {
	token     api.SecureString
	tokenFile string
	base      http.RoundTripper
}

func (t *tokenRoundTripper) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	token, err := t.resolveToken()
	if err != nil {
		return nil, err
	}

	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}
	cloned.Header.Set("Authorization", "Bearer "+token)

	return t.base.RoundTrip(cloned)
}

func (t *tokenRoundTripper) resolveToken() (string, error) {
	if t.tokenFile == "" {
		token := strings.TrimSpace(t.token.Value())
		if token == "" {
			return "", fmt.Errorf("configured bearer token is empty")
		}
		return token, nil
	}

	data, err := os.ReadFile(t.tokenFile)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}

	return token, nil
}
