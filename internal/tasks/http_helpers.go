package tasks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
)

func sendHTTPrequest(ctx context.Context, repoSpec domain.RepositorySpec, repoURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", repoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	repoHttpSpec, err := repoSpec.AsHttpRepoSpec()
	if err != nil {
		return nil, err
	}

	req, tlsConfig, err := buildHttpRepoRequestAuth(repoHttpSpec, req)
	if err != nil {
		return nil, fmt.Errorf("error building request authentication: %w", err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	return body, nil
}

func buildHttpRepoRequestAuth(repoHttpSpec domain.HttpRepoSpec, req *http.Request) (*http.Request, *tls.Config, error) {
	ctx := req.Context()
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// HttpConfig is optional - if not set, return default TLS config with no auth
	if repoHttpSpec.HttpConfig == nil {
		return req, tlsConfig, nil
	}

	if repoHttpSpec.HttpConfig.Username != nil && repoHttpSpec.HttpConfig.Password != nil {
		decryptedPassword, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(*repoHttpSpec.HttpConfig.Password))
		if err != nil {
			return nil, tlsConfig, fmt.Errorf("decrypt HTTP password: %w", err)
		}
		req.SetBasicAuth(*repoHttpSpec.HttpConfig.Username, string(decryptedPassword))
	}
	if repoHttpSpec.HttpConfig.Token != nil {
		decryptedToken, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(*repoHttpSpec.HttpConfig.Token))
		if err != nil {
			return nil, tlsConfig, fmt.Errorf("decrypt HTTP token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+string(decryptedToken))
	}
	if repoHttpSpec.HttpConfig.TlsCrt != nil && repoHttpSpec.HttpConfig.TlsKey != nil {
		decryptedCrt, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(*repoHttpSpec.HttpConfig.TlsCrt))
		if err != nil {
			return nil, tlsConfig, fmt.Errorf("decrypt TLS cert: %w", err)
		}
		cert, err := base64.StdEncoding.DecodeString(string(decryptedCrt))
		if err != nil {
			return nil, tlsConfig, err
		}

		decryptedKey, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(*repoHttpSpec.HttpConfig.TlsKey))
		if err != nil {
			return nil, tlsConfig, fmt.Errorf("decrypt TLS key: %w", err)
		}
		key, err := base64.StdEncoding.DecodeString(string(decryptedKey))
		if err != nil {
			return nil, tlsConfig, err
		}

		tlsPair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, tlsConfig, err
		}

		tlsConfig.Certificates = []tls.Certificate{tlsPair}
	}

	if repoHttpSpec.HttpConfig.CaCrt != nil {
		ca, err := base64.StdEncoding.DecodeString(*repoHttpSpec.HttpConfig.CaCrt)
		if err != nil {
			return nil, tlsConfig, err
		}

		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			return nil, tlsConfig, err
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		rootCAs.AppendCertsFromPEM(ca)
		tlsConfig.RootCAs = rootCAs
	}
	if repoHttpSpec.HttpConfig.SkipServerVerification != nil {
		tlsConfig.InsecureSkipVerify = *repoHttpSpec.HttpConfig.SkipServerVerification
	}

	return req, tlsConfig, nil
}
