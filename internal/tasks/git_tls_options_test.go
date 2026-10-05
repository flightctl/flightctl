package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
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
	t.Run("When the URL uses HTTP it should not apply HTTPS options", func(t *testing.T) {
		cfg := &domain.HttpConfig{SkipServerVerification: lo.ToPtr(true), CaCrt: lo.ToPtr("invalid-base64")}
		repo := fixture.repository(t, nil)
		require.NoError(t, repo.Spec.FromGitRepoSpec(domain.GitRepoSpec{Type: domain.GitRepoSpecTypeGit, Url: "http://example.com/repo.git", HttpConfig: cfg}))
		options, err := getGitOptions(context.Background(), repo, nil)
		require.NoError(t, err)
		require.False(t, options.InsecureSkipTLS)
		require.Empty(t, options.CABundle)
	})

	other := newGitHTTPSFixture(t, "client-other")
	cases := []struct {
		name string
		cfg  *domain.HttpConfig
	}{
		{name: "When the CA encoding is invalid it should reject the configuration", cfg: &domain.HttpConfig{CaCrt: lo.ToPtr("invalid-base64")}},
		{name: "When the client certificate encoding is invalid it should reject the configuration", cfg: &domain.HttpConfig{TlsCrt: lo.ToPtr("invalid-base64"), TlsKey: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.clientKeyPEM))}},
		{name: "When the client key encoding is invalid it should reject the configuration", cfg: &domain.HttpConfig{TlsCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.clientCertPEM)), TlsKey: lo.ToPtr("invalid-base64")}},
		{name: "When the client certificate and key do not match it should reject the configuration", cfg: &domain.HttpConfig{TlsCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.clientCertPEM)), TlsKey: lo.ToPtr(base64.StdEncoding.EncodeToString(other.clientKeyPEM))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, err := getGitOptions(context.Background(), fixture.repository(t, tc.cfg), nil)
			require.Error(t, err)
			require.Nil(t, options)
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
