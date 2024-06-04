package repotester

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"net/http"
	"regexp"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

// Ref: https://github.com/git/git/blob/master/Documentation/urls.txt#L37
var scpLikeUrlRegExp = regexp.MustCompile(`^(?:(?P<user>[^@]+)@)?(?P<host>[^:\s]+):(?:(?P<port>[0-9]{1,5}):)?(?P<path>[^\\].*)$`)

type API interface {
	Test()
}

type RepoTester struct {
	log                    logrus.FieldLogger
	repoStore              store.Repository
	TypeSpecificRepoTester TypeSpecificRepoTester
}

func NewRepoTester(log logrus.FieldLogger, store store.Store) *RepoTester {
	return &RepoTester{
		log:                    log,
		repoStore:              store.Repository(),
		TypeSpecificRepoTester: &GitRepoTester{},
	}
}

func (r *RepoTester) TestRepositories() {
	reqid.OverridePrefix("repotester")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running RepoTester")

	repositories, err := r.repoStore.ListIgnoreOrg()
	if err != nil {
		log.Errorf("error fetching repositories: %s", err)
		return
	}

	for i := range repositories {
		repository := repositories[i]
		accessErr := r.TypeSpecificRepoTester.TestAccess(&repository)

		err := r.SetAccessCondition(repository, accessErr)
		if err != nil {
			log.Errorf("Failed to update repository status for %s: %v", repository.Name, err)
		}
	}
}

func configureRepoHTTPSClient(httpConfig api.GitHttpConfig) error {
	tlsConfig := tls.Config{} //nolint:gosec
	if httpConfig.SkipServerVerification != nil {
		tlsConfig.InsecureSkipVerify = *httpConfig.SkipServerVerification //nolint:gosec
	}

	if httpConfig.TlsCrt != nil && httpConfig.TlsKey != nil {
		cert, err := b64.StdEncoding.DecodeString(*httpConfig.TlsCrt)
		if err != nil {
			return err
		}

		key, err := b64.StdEncoding.DecodeString(*httpConfig.TlsKey)
		if err != nil {
			return err
		}

		tlsPair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return err
		}

		tlsConfig.Certificates = []tls.Certificate{tlsPair}
	}

	if httpConfig.CaCrt != nil {
		ca, err := b64.StdEncoding.DecodeString(*httpConfig.CaCrt)
		if err != nil {
			return err
		}

		rootCAs, _ := x509.SystemCertPool()
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		rootCAs.AppendCertsFromPEM(ca)
		tlsConfig.RootCAs = rootCAs
	}

	gitclient.InstallProtocol("https", githttp.NewClient(
		&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tlsConfig,
			},
		},
	))
	return nil
}

// Read repository's ssh/http config and create transport.AuthMethod.
// If no ssh/http config is defined a nil is returned.
func GetAuth(repository *model.Repository) (transport.AuthMethod, error) {
	_, err := repository.Spec.Data.GetGitGenericRepoSpec()
	if err == nil {
		return nil, nil
	}
	sshSpec, err := repository.Spec.Data.GetGitSshRepoSpec()
	if err == nil {
		var auth *gitssh.PublicKeys
		if sshSpec.SshConfig.SshPrivateKey != nil {
			sshPrivateKey, err := b64.StdEncoding.DecodeString(*sshSpec.SshConfig.SshPrivateKey)
			if err != nil {
				return nil, err
			}

			password := ""
			if sshSpec.SshConfig.PrivateKeyPassphrase != nil {
				password = *sshSpec.SshConfig.PrivateKeyPassphrase
			}
			user := ""
			repoSubmatch := scpLikeUrlRegExp.FindStringSubmatch(sshSpec.Repo)
			if len(repoSubmatch) > 1 {
				user = repoSubmatch[1]
			}
			auth, err = gitssh.NewPublicKeys(user, sshPrivateKey, password)
			if err != nil {
				return nil, err
			}
			if sshSpec.SshConfig.SkipServerVerification != nil && *sshSpec.SshConfig.SkipServerVerification {
				auth.HostKeyCallbackHelper = gitssh.HostKeyCallbackHelper{
					HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
				}
			}
		}
		if sshSpec.SshConfig.SkipServerVerification != nil && *sshSpec.SshConfig.SkipServerVerification {
			if auth == nil {
				auth = &gitssh.PublicKeys{}
			}
			auth.HostKeyCallbackHelper = gitssh.HostKeyCallbackHelper{
				HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
			}
		}
		return auth, nil
	} else {
		httpSpec, err := repository.Spec.Data.GetGitHttpRepoSpec()
		if err == nil {
			if strings.HasPrefix(httpSpec.Repo, "https") {
				err := configureRepoHTTPSClient(httpSpec.HttpConfig)
				if err != nil {
					return nil, err
				}
			}
			if httpSpec.HttpConfig.Username != nil && httpSpec.HttpConfig.Password != nil {
				auth := &githttp.BasicAuth{
					Username: *httpSpec.HttpConfig.Username,
					Password: *httpSpec.HttpConfig.Password,
				}
				return auth, nil
			}
		}
	}
	return nil, nil
}

type TypeSpecificRepoTester interface {
	TestAccess(repository *model.Repository) error
}

type GitRepoTester struct {
}

func (r *GitRepoTester) TestAccess(repository *model.Repository) error {
	repoURL, err := repository.Spec.Data.GetRepoURL()
	if err != nil {
		return err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name:  repository.Name,
		URLs:  []string{repoURL},
		Fetch: []config.RefSpec{"HEAD"},
	})

	listOps := &git.ListOptions{}
	auth, err := GetAuth(repository)
	if err != nil {
		return err
	}

	listOps.Auth = auth
	_, err = remote.List(listOps)
	return err
}

func (r *RepoTester) SetAccessCondition(repository model.Repository, err error) error {
	if repository.Status == nil {
		repository.Status = model.MakeJSONField(api.RepositoryStatus{Conditions: &[]api.Condition{}})
	}
	if repository.Status.Data.Conditions == nil {
		repository.Status.Data.Conditions = &[]api.Condition{}
	}
	changed := api.SetStatusConditionByError(repository.Status.Data.Conditions, api.RepositoryAccessible, "Accessible", "Inaccessible", err)
	if changed {
		return r.repoStore.UpdateStatusIgnoreOrg(&repository)
	}
	return nil
}
