package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func gitTestCredential(value string) string {
	return encryptString(value)
}

func TestGitHTTPSOptions(t *testing.T) {
	fixture := newGitHTTPSFixture(t, "client-options")
	t.Run("When HTTPS credentials are configured it should return independent options", func(t *testing.T) {
		cfg := fixture.mutualTLSConfig()
		cfg.TlsCrt = lo.ToPtr(gitTestCredential(*cfg.TlsCrt))
		cfg.TlsKey = lo.ToPtr(gitTestCredential(*cfg.TlsKey))
		cfg.Token = lo.ToPtr(gitTestCredential("repository-token"))
		cfg.SkipServerVerification = lo.ToPtr(true)
		originalTransport := gitclient.Protocols["https"]
		options, err := getGitOptions(context.Background(), fixture.repository(t, cfg), nil)
		require.NoError(t, err)
		require.True(t, originalTransport == gitclient.Protocols["https"])
		require.True(t, options.InsecureSkipTLS)
		require.True(t, bytes.Equal(fixture.clientCertPEM, options.ClientCert))
		require.True(t, bytes.Equal(fixture.clientKeyPEM, options.ClientKey))
		require.True(t, bytes.Equal(fixture.caPEM, options.CABundle))
		require.IsType(t, &githttp.TokenAuth{}, options.Auth)
		require.Equal(t, "repository-token", options.Auth.(*githttp.TokenAuth).Token)

		defaults, err := getGitOptions(context.Background(), fixture.repository(t, nil), nil)
		require.NoError(t, err)
		require.NotSame(t, options, defaults)
		require.False(t, defaults.InsecureSkipTLS)
		require.Empty(t, defaults.ClientCert)
		require.Empty(t, defaults.ClientKey)
		require.Empty(t, defaults.CABundle)
		require.Nil(t, defaults.Auth)
	})
	t.Run("When basic authentication is configured it should preserve the credentials", func(t *testing.T) {
		cfg := &domain.HttpConfig{Username: lo.ToPtr("repository-user"), Password: lo.ToPtr(gitTestCredential("repository-password"))}
		options, err := getGitOptions(context.Background(), fixture.repository(t, cfg), nil)
		require.NoError(t, err)
		require.IsType(t, &githttp.BasicAuth{}, options.Auth)
		require.Equal(t, "repository-user", options.Auth.(*githttp.BasicAuth).Username)
		require.Equal(t, "repository-password", options.Auth.(*githttp.BasicAuth).Password)
	})
	urlCases := []struct {
		name  string
		url   string
		https bool
	}{
		{name: "When the URL uses HTTP it should not apply HTTPS options", url: "http://example.com/repo.git"},
		{name: "When the URL uses uppercase HTTPS it should apply HTTPS options", url: "HTTPS://example.com/repo.git", https: true},
		{name: "When the URL uses mixed case HTTPS it should apply HTTPS options", url: "hTtPs://example.com/repo.git", https: true},
		{name: "When the URL uses an HTTPS lookalike scheme it should not apply HTTPS options", url: "httpsx://example.com/repo.git"},
		{name: "When the URL is malformed it should not apply HTTPS options", url: "https://[::1/repo.git"},
	}
	for _, tc := range urlCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &domain.HttpConfig{SkipServerVerification: lo.ToPtr(true), CaCrt: lo.ToPtr("invalid-base64")}
			if tc.https {
				cfg.CaCrt = lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.caPEM))
			}
			repo := fixture.repository(t, nil)
			require.NoError(t, repo.Spec.FromGitRepoSpec(domain.GitRepoSpec{Type: domain.GitRepoSpecTypeGit, Url: tc.url, HttpConfig: cfg}))
			options, err := getGitOptions(context.Background(), repo, nil)
			require.NoError(t, err)
			require.Equal(t, tc.https, options.InsecureSkipTLS)
			if tc.https {
				require.True(t, bytes.Equal(fixture.caPEM, options.CABundle))
			} else {
				require.Empty(t, options.CABundle)
			}
		})
	}

	other := newGitHTTPSFixture(t, "client-other")
	t.Run("When the CA bundle contains multiple certificates it should preserve the complete bundle", func(t *testing.T) {
		bundle := bytes.Join([][]byte{fixture.caPEM, other.caPEM}, nil)
		cfg := &domain.HttpConfig{CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(bundle))}
		options, err := getGitOptions(context.Background(), fixture.repository(t, cfg), nil)
		require.NoError(t, err)
		require.True(t, bytes.Equal(bundle, options.CABundle))
	})
	cases := []struct {
		name          string
		cfg           *domain.HttpConfig
		errorContext  string
		base64Failure bool
		wrappedError  bool
	}{
		{name: "When the CA encoding is invalid it should reject the configuration", cfg: &domain.HttpConfig{CaCrt: lo.ToPtr("invalid-base64")}, errorContext: "decode CA bundle", base64Failure: true},
		{name: "When the CA bundle is empty it should reject the configuration", cfg: &domain.HttpConfig{CaCrt: lo.ToPtr("")}, errorContext: "parse CA bundle: no valid PEM certificates"},
		{name: "When the CA bundle is not PEM it should reject the configuration", cfg: &domain.HttpConfig{CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString([]byte("not a certificate")))}, errorContext: "parse CA bundle: no valid PEM certificates"},
		{name: "When the CA bundle contains an invalid certificate it should reject the configuration", cfg: &domain.HttpConfig{CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid certificate")})))}, errorContext: "parse CA bundle: no valid PEM certificates"},
		{name: "When the CA bundle has no certificate blocks it should reject the configuration", cfg: &domain.HttpConfig{CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("not a certificate")})))}, errorContext: "parse CA bundle: no valid PEM certificates"},
		{name: "When the client certificate encoding is invalid it should reject the configuration", cfg: &domain.HttpConfig{TlsCrt: lo.ToPtr("invalid-base64"), TlsKey: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.clientKeyPEM))}, base64Failure: true},
		{name: "When the client key encoding is invalid it should reject the configuration", cfg: &domain.HttpConfig{TlsCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.clientCertPEM)), TlsKey: lo.ToPtr("invalid-base64")}, base64Failure: true},
		{name: "When the client certificate and key do not match it should reject the configuration", cfg: &domain.HttpConfig{TlsCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.clientCertPEM)), TlsKey: lo.ToPtr(base64.StdEncoding.EncodeToString(other.clientKeyPEM))}, errorContext: "validate TLS client certificate/key", wrappedError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, err := getGitOptions(context.Background(), fixture.repository(t, tc.cfg), nil)
			require.Error(t, err)
			require.Nil(t, options)
			if tc.errorContext != "" {
				require.ErrorContains(t, err, tc.errorContext)
			}
			if tc.base64Failure {
				var decodeErr base64.CorruptInputError
				require.ErrorAs(t, err, &decodeErr)
			}
			if tc.wrappedError {
				require.NotNil(t, errors.Unwrap(err))
			}
		})
	}
}

func gitHTTPSListOperations() []gitHTTPSOperation {
	return []gitHTTPSOperation{
		{name: "remote list", run: func(repo *domain.Repository) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			options, err := getGitOptions(ctx, repo, nil)
			if err != nil {
				return err
			}
			url, err := repo.Spec.GetRepoURL()
			if err != nil {
				return err
			}
			_, err = GitLsRemote(ctx, url, []string{"main"}, options)
			return err
		}},
		{name: "dependency probe", run: func(repo *domain.Repository) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			logger := logrus.New()
			logger.SetOutput(io.Discard)
			d := &DependencySyncGit{log: logger, lsRemote: GitLsRemote}
			group := []*model.GitDependencyProbe{{RepositoryName: *repo.Metadata.Name, Revision: "main", RepoSpec: model.MakeJSONField(repo.Spec)}}
			results := d.probeRepo(ctx, *repo.Metadata.Name, group)
			if len(results) != 1 {
				return fmt.Errorf("expected one dependency-probe result, got %d", len(results))
			}
			if results[0].probeErr == "" {
				return nil
			}
			return errors.New(results[0].probeErr)
		}},
	}
}

func TestGitHTTPSListIsolation(t *testing.T) {
	testGitHTTPSIsolation(t, gitHTTPSListOperations())
}

func TestGitHTTPSListIsolationConcurrent(t *testing.T) {
	testGitHTTPSIsolationConcurrent(t, gitHTTPSListOperations())
}

func TestGitHTTPSListSuccess(t *testing.T) {
	const sha = "0123456789012345678901234567890123456789"
	advertisement := "001e# service=git-upload-pack\n0000"
	for _, ref := range []string{sha + " HEAD\x00symref=HEAD:refs/heads/main\n", sha + " refs/heads/main\n"} {
		advertisement += fmt.Sprintf("%04x%s", len(ref)+4, ref)
	}
	advertisement += "0000"
	fixture := newGitHTTPSFixtureWithAdvertisement(t, "client-list-success", advertisement)
	for _, operation := range gitHTTPSListOperations() {
		t.Run("When "+operation.name+" resolves a revision it should report success", func(t *testing.T) {
			before := len(fixture.clientNames())
			require.NoError(t, operation.run(fixture.repository(t, fixture.mutualTLSConfig())))
			require.Equal(t, []string{"client-list-success"}, fixture.clientNames()[before:])
		})
	}
}
