package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Resource Types ==========

type Repository = v1beta1.Repository
type RepositoryList = v1beta1.RepositoryList
type RepositorySpec = v1beta1.RepositorySpec
type RepositoryStatus = v1beta1.RepositoryStatus

// ========== Repository Spec Types ==========

type GitRepoSpec = v1beta1.GitRepoSpec
type OciRepoSpec = v1beta1.OciRepoSpec
type HttpRepoSpec = v1beta1.HttpRepoSpec
type SshConfig = v1beta1.SshConfig
type HttpConfig = v1beta1.HttpConfig

// ========== OCI Auth Types ==========

type OciAuth = v1beta1.OciAuth
type OciAuthType = v1beta1.OciAuthType
type DockerAuth = v1beta1.DockerAuth

const (
	OciAuthTypeDocker = v1beta1.Docker

	// Direct alias for compatibility
	Docker = v1beta1.Docker
)

// ========== OCI Repo Spec Types ==========

type OciRepoSpecAccessMode = v1beta1.OciRepoSpecAccessMode
type OciRepoSpecScheme = v1beta1.OciRepoSpecScheme

const (
	OciRepoAccessModeRead      = v1beta1.Read
	OciRepoAccessModeReadWrite = v1beta1.ReadWrite
	OciRepoSchemeHttp          = v1beta1.Http
	OciRepoSchemeHttps         = v1beta1.Https
)

// ========== Repo Spec Type ==========

type RepoSpecType = v1beta1.RepoSpecType
type GitRepoSpecType = v1beta1.GitRepoSpecType
type HttpRepoSpecType = v1beta1.HttpRepoSpecType
type OciRepoSpecType = v1beta1.OciRepoSpecType

const (
	RepoSpecTypeGit  = v1beta1.RepoSpecTypeGit
	RepoSpecTypeHttp = v1beta1.RepoSpecTypeHttp
	RepoSpecTypeOci  = v1beta1.RepoSpecTypeOci

	// Specific type constants for strict oneOf discrimination
	GitRepoSpecTypeGit   = v1beta1.GitRepoSpecTypeGit
	HttpRepoSpecTypeHttp = v1beta1.HttpRepoSpecTypeHttp
	OciRepoSpecTypeOci   = v1beta1.OciRepoSpecTypeOci
)
