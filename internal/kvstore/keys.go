package kvstore

import (
	"crypto/md5" //nolint: gosec
	"fmt"

	"github.com/google/uuid"
)

type TemplateVersionKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
}

func (k *TemplateVersionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/", k.OrgID, k.Fleet, k.TemplateVersion)
}

type RepositoryUrlKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
}

func (k *RepositoryUrlKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/repo-url/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository)
}

type GitRevisionKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
	TargetRevision  string
}

func (k *GitRevisionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/git-hash/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository, k.TargetRevision)
}

type GitContentsKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
	TargetRevision  string
	Path            string
}

func (k *GitContentsKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/git-data/%s/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository, k.TargetRevision, k.Path)
}

type K8sSecretKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Namespace       string
	Name            string
}

func (k *K8sSecretKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/k8ssecret-data/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Namespace, k.Name)
}

type HttpKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	URL             string
}

func (k *HttpKey) ComposeKey() string {
	md5sum := md5.Sum([]byte(k.URL)) //nolint: gosec
	return fmt.Sprintf("v1/%s/%s/%s/http-data/%x", k.OrgID, k.Fleet, k.TemplateVersion, md5sum)
}
