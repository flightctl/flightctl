package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "repotester-test")
	if err != nil {
		panic(err)
	}

	key, err := crypto.GenerateAES256Key()
	if err != nil {
		panic(err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
		panic(err)
	}

	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		Keys:        []config.EncryptionKeyConfig{{ID: "test", Path: keyPath}},
		ActiveKeyID: "test",
	}

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	if err := encryption.InitGlobalEncryption(logger, cfg); err != nil {
		panic(err)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// makeTestOciRepo builds a Repository pointing at a test HTTP server.
func makeTestOciRepo(t *testing.T, server *httptest.Server, auth *domain.OciAuth) *domain.Repository {
	t.Helper()
	scheme := domain.OciRepoSchemeHttp
	ociSpec := domain.OciRepoSpec{
		Registry: server.Listener.Addr().String(),
		Type:     domain.OciRepoSpecTypeOci,
		Scheme:   &scheme,
		OciAuth:  auth,
	}
	spec := domain.RepositorySpec{}
	err := spec.FromOciRepoSpec(ociSpec)
	require.NoError(t, err)
	return &domain.Repository{Spec: spec}
}

// makeDockerOciAuth creates an OciAuth with docker credentials.
// The password is encrypted to match what the GORM plugin produces on save.
func makeDockerOciAuth(username, password string) *domain.OciAuth {
	encrypted, err := encryption.Encrypt(context.Background(), []byte(password))
	if err != nil {
		panic(fmt.Sprintf("makeDockerOciAuth encrypt: %v", err))
	}
	auth := &domain.OciAuth{}
	err = auth.FromDockerAuth(domain.DockerAuth{
		AuthType: domain.Docker,
		Username: username,
		Password: string(encrypted),
	})
	if err != nil {
		panic(fmt.Sprintf("makeDockerOciAuth: %v", err))
	}
	return auth
}

// basicAuthRegistryHandler returns a handler that challenges clients with Basic auth
// and accepts only the given username/password on /v2/.
func basicAuthRegistryHandler(expectedUsername, expectedPassword string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if ok && username == expectedUsername && password == expectedPassword {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Www-Authenticate", `Basic realm="Registry Realm"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
}

func TestOciRepoTester_TestAccess(t *testing.T) {
	tester := &OciRepoTester{}

	t.Run("When registry returns 200 without auth it returns nil", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		repo := makeTestOciRepo(t, server, nil)
		require.NoError(t, tester.TestAccess(t.Context(), repo))
	})

	t.Run("When registry returns 401 with Basic challenge and valid credentials it returns nil", func(t *testing.T) {
		server := httptest.NewServer(basicAuthRegistryHandler("user", "pass"))
		defer server.Close()

		repo := makeTestOciRepo(t, server, makeDockerOciAuth("user", "pass"))
		require.NoError(t, tester.TestAccess(t.Context(), repo))
	})

	t.Run("When registry returns 401 with Basic challenge and wrong credentials it returns an auth error", func(t *testing.T) {
		server := httptest.NewServer(basicAuthRegistryHandler("user", "correct"))
		defer server.Close()

		repo := makeTestOciRepo(t, server, makeDockerOciAuth("user", "wrong"))
		err := tester.TestAccess(t.Context(), repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authentication failed")
	})

	t.Run("When registry returns 401 with Basic challenge and no credentials it returns an error", func(t *testing.T) {
		server := httptest.NewServer(basicAuthRegistryHandler("user", "pass"))
		defer server.Close()

		repo := makeTestOciRepo(t, server, nil)
		err := tester.TestAccess(t.Context(), repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no credentials")
	})

	t.Run("When registry returns 401 with Bearer challenge and valid credentials it returns nil", func(t *testing.T) {
		const validToken = "valid-token"
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/token":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				body, _ := json.Marshal(map[string]string{"token": validToken})
				_, _ = w.Write(body)
			default:
				if r.Header.Get("Authorization") == "Bearer "+validToken {
					w.WriteHeader(http.StatusOK)
					return
				}
				realm := fmt.Sprintf("http://%s/token", server.Listener.Addr().String())
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="test"`, realm))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer server.Close()

		repo := makeTestOciRepo(t, server, makeDockerOciAuth("user", "pass"))
		require.NoError(t, tester.TestAccess(t.Context(), repo))
	})

	t.Run("When registry returns 401 with an unsupported auth scheme it returns an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Www-Authenticate", `Digest realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		repo := makeTestOciRepo(t, server, nil)
		err := tester.TestAccess(t.Context(), repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported auth type")
	})

	t.Run("When registry returns 401 with Basic challenge and empty credentials it returns an error", func(t *testing.T) {
		server := httptest.NewServer(basicAuthRegistryHandler("user", "pass"))
		defer server.Close()

		repo := makeTestOciRepo(t, server, makeDockerOciAuth("", ""))
		err := tester.TestAccess(t.Context(), repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "credentials are incomplete")
	})

	t.Run("When registry returns a non-401 non-200 status on Basic auth it returns an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Www-Authenticate", `Basic realm="Registry Realm"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		repo := makeTestOciRepo(t, server, makeDockerOciAuth("user", "pass"))
		err := tester.TestAccess(t.Context(), repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authentication failed")
	})
}
