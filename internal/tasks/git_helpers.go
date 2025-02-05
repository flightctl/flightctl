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
	api "github.com/flightctl/flightctl/api/v1alpha1"
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
)

// Ref: https://github.com/git/git/blob/master/Documentation/urls.txt#L37
var scpLikeUrlRegExp = regexp.MustCompile(`^(?:(?P<user>[^@]+)@)?(?P<host>[^:\s]+):(?:(?P<port>[0-9]{1,5}):)?(?P<path>[^\\].*)$`)

// a function to clone a git repo, for mockable unit testing
type cloneGitRepoFunc func(repo *api.Repository, revision *string, depth *int) (billy.Filesystem, string, error)

func CloneGitRepo(repo *api.Repository, revision *string, depth *int) (billy.Filesystem, string, error) {
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
	auth, err := GetAuth(repo)
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

// Read repository's ssh/http config and create transport.AuthMethod.
// If no ssh/http config is defined a nil is returned.
func GetAuth(repository *api.Repository) (transport.AuthMethod, error) {
	_, err := repository.Spec.GetGenericRepoSpec()
	if err == nil {
		return nil, nil
	}
	sshSpec, err := repository.Spec.GetSshRepoSpec()
	if err == nil {
		var auth *gitssh.PublicKeys
		if sshSpec.SshConfig.SshPrivateKey != nil {
			sshPrivateKey, err := base64.StdEncoding.DecodeString(*sshSpec.SshConfig.SshPrivateKey)
			if err != nil {
				return nil, err
			}

			password := ""
			if sshSpec.SshConfig.PrivateKeyPassphrase != nil {
				password = *sshSpec.SshConfig.PrivateKeyPassphrase
			}
			user := ""
			repoSubmatch := scpLikeUrlRegExp.FindStringSubmatch(sshSpec.Url)
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
		httpSpec, err := repository.Spec.GetHttpRepoSpec()
		if err == nil {
			if strings.HasPrefix(httpSpec.Url, "https") {
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

func configureRepoHTTPSClient(httpConfig api.HttpConfig) error {
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

// ConvertFileSystemToIgnition converts a filesystem to an ignition config
// The filesystem is expected to be a git repo, and the path is the root of the repo
// The function will recursively walk the filesystem and add all files to the ignition config
// In case user provides file path we will add file as "/file-name"
// In case user provides folder we will drop folder path add all files and subfolder with subfolder paths, like
// Example: ConvertFileSystemToIgnition(mfs, "/test-path) will go through all subfolder and files and build ignition paths like
// /etc/motd, /etc/config/file.yaml
// The function will return an error if the path does not exist or if there is an error reading the filesystem
func ConvertFileSystemToIgnition(mfs billy.Filesystem, path string, mountPath string) (*config_latest_types.Config, error) {
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
		err = addGitDirToIgnitionConfig(mfs, path, mountPath, files, wrapper)
		if err != nil {
			return nil, fmt.Errorf("failed converting directory %s to ignition: %w", path, err)
		}
	} else {
		err = addGitFileToIgnitionConfig(mfs, path, filepath.Join(mountPath, fileInfo.Name()), fileInfo, wrapper)
		if err != nil {
			return nil, fmt.Errorf("failed converting file %s to ignition: %w", path, err)
		}
	}

	ignition := wrapper.AsIgnitionConfig()
	return &ignition, nil
}

func CloneGitRepoToIgnition(repo *api.Repository, revision string, path string, mountPath string) (*config_latest_types.Config, string, error) {
	mfs, hash, err := CloneGitRepo(repo, &revision, nil)
	if err != nil {
		return nil, "", err
	}
	ign, err := ConvertFileSystemToIgnition(mfs, path, mountPath)
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

	wrapper.SetFile(ignPath, fileContents, int(fileInfo.Mode()), false, nil, nil)
	return nil
}
