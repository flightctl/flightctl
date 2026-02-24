package tasks

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	sshcrypto "github.com/flightctl/flightctl/internal/ssh"
	"github.com/flightctl/flightctl/pkg/ignition"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	gitplumbing "github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gitmemory "github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Ref: https://github.com/git/git/blob/master/Documentation/urls.txt#L37
var scpLikeUrlRegExp = regexp.MustCompile(`^(?:(?P<user>[^@]+)@)?(?P<host>[^:\s]+):(?:(?P<port>[0-9]{1,5}):)?(?P<path>[^\\].*)$`)

// a function to clone a git repo, for mockable unit testing
type cloneGitRepoFunc func(repo *domain.Repository, revision *string, depth *int, cfg *config.Config) (billy.Filesystem, string, error)

func CloneGitRepo(repo *domain.Repository, revision *string, depth *int, cfg *config.Config) (billy.Filesystem, string, error) {
	storage := gitmemory.NewStorage()
	mfs := memfs.New()
	repoURL, err := repo.Spec.GetRepoURL()
	if err != nil {
		return nil, "", err
	}
	opts := &git.CloneOptions{
		URL: repoURL,
	}
	if depth != nil {
		opts.Depth = *depth
	}
	auth, err := GetAuth(repo, cfg)
	if err != nil {
		return nil, "", err
	}
	opts.Auth = auth
	hash := ""
	if revision != nil {
		referenceIsHash := gitplumbing.IsHash(*revision)
		if !referenceIsHash {
			opts.ReferenceName = gitplumbing.ReferenceName(*revision)
		} else {
			hash = *revision
		}
	}
	gitRepo, err := git.Clone(storage, mfs, opts)
	if err != nil {
		return nil, "", fmt.Errorf("failed cloning git repo: %w", err)
	}
	if hash != "" {
		worktree, err := gitRepo.Worktree()
		if err != nil {
			return nil, "", fmt.Errorf("failed getting git repo worktree: %w", err)
		}
		err = worktree.Checkout(&git.CheckoutOptions{Hash: gitplumbing.NewHash(hash), Force: true})
		if err != nil {
			return nil, "", fmt.Errorf("failed checking out specified git hash %s: %w", gitplumbing.NewHash(hash), err)
		}
	} else {
		head, err := gitRepo.Head()
		if err != nil {
			return nil, "", fmt.Errorf("failed getting git repo head: %w", err)
		}
		hash = head.Hash().String()
	}

	return mfs, hash, nil
}

// sshAuthWrapper wraps an SSH auth method to apply additional crypto settings
type sshAuthWrapper struct {
	wrapped        transport.AuthMethod
	cryptoSettings sshcrypto.SSHCryptoSettings
}

func (w *sshAuthWrapper) Name() string {
	return w.wrapped.Name()
}

func (w *sshAuthWrapper) String() string {
	return w.wrapped.String()
}

func (w *sshAuthWrapper) ClientConfig() (*ssh.ClientConfig, error) {
	// Get the base config from the wrapped auth method
	sshAuth, ok := w.wrapped.(interface {
		ClientConfig() (*ssh.ClientConfig, error)
	})
	if !ok {
		return nil, fmt.Errorf("wrapped auth does not support ClientConfig()")
	}

	cfg, err := sshAuth.ClientConfig()
	if err != nil {
		return nil, err
	}

	// Apply crypto settings to the config
	w.cryptoSettings.ApplyCryptoSettingsToClientConfig(cfg)
	return cfg, nil
}

// wrapAuthWithCryptoSettings wraps an auth method to apply SSH crypto settings
func wrapAuthWithCryptoSettings(auth transport.AuthMethod, settings sshcrypto.SSHCryptoSettings) transport.AuthMethod {
	// If no settings are configured, return the auth as-is
	if len(settings.KeyExchanges) == 0 && len(settings.Ciphers) == 0 && len(settings.MACs) == 0 {
		return auth
	}

	return &sshAuthWrapper{
		wrapped:        auth,
		cryptoSettings: settings,
	}
}

// Read repository's ssh/http config and create transport.AuthMethod.
// If no ssh/http config is defined a nil is returned.
func GetAuth(repository *domain.Repository, cfg *config.Config) (transport.AuthMethod, error) {
	gitSpec, err := repository.Spec.AsGitRepoSpec()
	if err != nil {
		// Not a Git repo spec, no auth
		return nil, nil
	}

	// Handle SSH authentication
	if gitSpec.SshConfig != nil {
		var auth *gitssh.PublicKeys
		if gitSpec.SshConfig.SshPrivateKey != nil {
			sshPrivateKey, err := base64.StdEncoding.DecodeString(*gitSpec.SshConfig.SshPrivateKey)
			if err != nil {
				return nil, err
			}

			password := ""
			if gitSpec.SshConfig.PrivateKeyPassphrase != nil {
				password = *gitSpec.SshConfig.PrivateKeyPassphrase
			}
			user := ""
			repoSubmatch := scpLikeUrlRegExp.FindStringSubmatch(gitSpec.Url)
			if len(repoSubmatch) > 1 {
				user = repoSubmatch[1]
			}
			auth, err = gitssh.NewPublicKeys(user, sshPrivateKey, password)
			if err != nil {
				return nil, err
			}
		}

		// Configure host key verification
		if gitSpec.SshConfig.SkipServerVerification != nil && *gitSpec.SshConfig.SkipServerVerification {
			if auth == nil {
				auth = &gitssh.PublicKeys{}
			}
			auth.HostKeyCallbackHelper = gitssh.HostKeyCallbackHelper{
				HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
			}
		} else {
			callback, cbErr := buildKnownHostsCallback()
			if cbErr != nil {
				return nil, cbErr
			}
			if callback != nil {
				if auth == nil {
					auth = &gitssh.PublicKeys{}
				}
				auth.HostKeyCallbackHelper = gitssh.HostKeyCallbackHelper{HostKeyCallback: callback}
			}
		}

		// Apply SSH crypto algorithm configuration for FIPS compliance
		if auth != nil {
			cryptoSettings := sshcrypto.GetSSHCryptoSettings(cfg)
			// Set HostKeyAlgorithms directly on the auth struct
			auth.HostKeyAlgorithms = cryptoSettings.HostKeyAlgorithms
			// Wrap the auth to apply additional crypto settings
			return wrapAuthWithCryptoSettings(auth, cryptoSettings), nil
		}

		return auth, nil
	}

	// Handle HTTP authentication
	if gitSpec.HttpConfig != nil {
		if strings.HasPrefix(gitSpec.Url, "https") {
			err := configureRepoHTTPSClient(*gitSpec.HttpConfig)
			if err != nil {
				return nil, err
			}
		}
		if gitSpec.HttpConfig.Token != nil {
			auth := &githttp.TokenAuth{
				Token: *gitSpec.HttpConfig.Token,
			}
			return auth, nil
		}
		if gitSpec.HttpConfig.Username != nil && gitSpec.HttpConfig.Password != nil {
			auth := &githttp.BasicAuth{
				Username: *gitSpec.HttpConfig.Username,
				Password: *gitSpec.HttpConfig.Password,
			}
			return auth, nil
		}
	}

	// No auth config - public repository
	return nil, nil
}

func configureRepoHTTPSClient(httpConfig domain.HttpConfig) error {
	tlsConfig := tls.Config{} //nolint:gosec
	if httpConfig.SkipServerVerification != nil {
		tlsConfig.InsecureSkipVerify = *httpConfig.SkipServerVerification //nolint:gosec
	}

	if httpConfig.TlsCrt != nil && httpConfig.TlsKey != nil {
		cert, err := base64.StdEncoding.DecodeString(*httpConfig.TlsCrt)
		if err != nil {
			return err
		}

		key, err := base64.StdEncoding.DecodeString(*httpConfig.TlsKey)
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
		ca, err := base64.StdEncoding.DecodeString(*httpConfig.CaCrt)
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

func buildKnownHostsCallback() (ssh.HostKeyCallback, error) {
	cb, err := knownhosts.New("/etc/flightctl/ssh/known_hosts")
	if err != nil {
		return nil, fmt.Errorf("failed to load SSH known_hosts: %w", err)
	}
	return cb, nil
}

// ConvertFileSystemToIgnition converts a filesystem to an ignition config
// The filesystem is expected to be a git repo, and the path is the root of the repo
// The function will recursively walk the filesystem and add all files to the ignition config
// In case user provides file path we will add file as "/file-name"
// In case user provides folder we will drop folder path add all files and subfolder with subfolder paths, like
// Example: ConvertFileSystemToIgnition(mfs, "/test-path) will go through all subfolder and files and build ignition paths like
// /etc/motd, /etc/config/file.yaml
// The function will return an error if the path does not exist or if there is an error reading the filesystem
func ConvertFileSystemToIgnition(mfs billy.Filesystem, path string) (*config_latest_types.Config, error) {
	fileInfo, err := mfs.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed accessing path %s: %w", path, err)
	}

	wrapper, err := ignition.NewWrapper()
	if err != nil {
		return nil, fmt.Errorf("failed initializing ignition wrapper: %w", err)
	}

	if fileInfo.IsDir() {
		files, err := mfs.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("failed reading directory %s: %w", path, err)
		}
		err = addGitDirToIgnitionConfig(mfs, path, "/", files, wrapper)
		if err != nil {
			return nil, fmt.Errorf("failed converting directory %s to ignition: %w", path, err)
		}
	} else {
		err = addGitFileToIgnitionConfig(mfs, path, filepath.Join("/", fileInfo.Name()), fileInfo, wrapper)
		if err != nil {
			return nil, fmt.Errorf("failed converting file %s to ignition: %w", path, err)
		}
	}

	ignition := wrapper.AsIgnitionConfig()
	return &ignition, nil
}

func CloneGitRepoToIgnition(repo *domain.Repository, revision string, path string, cfg *config.Config) (*config_latest_types.Config, string, error) {
	mfs, hash, err := CloneGitRepo(repo, &revision, nil, cfg)
	if err != nil {
		return nil, "", err
	}
	ign, err := ConvertFileSystemToIgnition(mfs, path)
	if err != nil {
		return nil, "", err
	}
	return ign, hash, nil
}

func addGitDirToIgnitionConfig(mfs billy.Filesystem, fullPrefix, ignPrefix string, fileInfos []fs.FileInfo, wrapper ignition.Wrapper) error {
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			subdirFiles, err := mfs.ReadDir(filepath.Join(fullPrefix, fileInfo.Name()))
			if err != nil {
				return fmt.Errorf("failed reading directory %s: %w", fileInfo.Name(), err)
			}
			// recursion!
			err = addGitDirToIgnitionConfig(mfs, filepath.Join(fullPrefix, fileInfo.Name()), filepath.Join(ignPrefix, fileInfo.Name()), subdirFiles, wrapper)
			if err != nil {
				return err
			}
		} else {
			err := addGitFileToIgnitionConfig(mfs, filepath.Join(fullPrefix, fileInfo.Name()), filepath.Join(ignPrefix, fileInfo.Name()), fileInfo, wrapper)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func addGitFileToIgnitionConfig(mfs billy.Filesystem, fullPath, ignPath string, fileInfo fs.FileInfo, wrapper ignition.Wrapper) error {
	openFile, err := mfs.Open(fullPath)
	if err != nil {
		return err
	}
	defer openFile.Close()

	fileContents, err := io.ReadAll(openFile)
	if err != nil {
		return err
	}

	wrapper.SetFile(ignPath, fileContents, int(fileInfo.Mode()), false, "", "")
	return nil
}
