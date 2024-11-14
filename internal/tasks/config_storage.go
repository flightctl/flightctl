package tasks

import (
	"context"
	"crypto/md5" //nolint: gosec
	"fmt"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type TemplateVersionKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
}

func (k *TemplateVersionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/", k.OrgID, k.Fleet, k.TemplateVersion)
}

type ConfigStorageRepositoryUrlKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
}

func (k *ConfigStorageRepositoryUrlKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/repo/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository)
}

type ConfigStorageGitRevisionKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Repository      string
	TargetRevision  string
}

func (k *ConfigStorageGitRevisionKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/git/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Repository, k.TargetRevision)
}

type ConfigStorageK8sSecretKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	Namespace       string
	Name            string
}

func (k *ConfigStorageK8sSecretKey) ComposeKey() string {
	return fmt.Sprintf("v1/%s/%s/%s/k8s/%s/%s", k.OrgID, k.Fleet, k.TemplateVersion, k.Namespace, k.Name)
}

type ConfigStorageHttpKey struct {
	OrgID           uuid.UUID
	Fleet           string
	TemplateVersion string
	URL             string
}

func (k *ConfigStorageHttpKey) ComposeKey() string {
	md5sum := md5.Sum([]byte(k.URL)) //nolint: gosec
	return fmt.Sprintf("v1/%s/%s/%s/http/%x", k.OrgID, k.Fleet, k.TemplateVersion, md5sum)
}

type ConfigStorage interface {
	Close()
	SetNX(ctx context.Context, key string, value []byte) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error)
	DeleteKeysForTemplateVersion(ctx context.Context, key string) error
	DeleteAllKeys(ctx context.Context)
	PrintAllKeys(ctx context.Context) // For debugging
}

type configStorage struct {
	client         valkey.Client
	getSetNxScript *valkey.Lua
}

func NewConfigStorage(hostname string, port uint) (ConfigStorage, error) {
	client, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{fmt.Sprintf("%s:%d", hostname, port)}})
	if err != nil {
		return nil, err
	}

	// Lua script to get the value if it exists, otherwise set and return it
	luaScript := `
		local value = redis.call('get', KEYS[1])
		if not value then
			redis.call('set', KEYS[1], ARGV[1], 'NX')
			value = ARGV[1]
		end
		return value
	`
	getSetNxScript := valkey.NewLuaScript(luaScript)

	return &configStorage{client: client, getSetNxScript: getSetNxScript}, nil
}

func (s *configStorage) Close() {
	s.client.Close()
}

func (s *configStorage) DeleteAllKeys(ctx context.Context) {
	s.client.Do(ctx, s.client.B().Flushall().Build())
}

// Sets the key to value only if the key does Not eXist. Returns a boolean indicating if the value was updated by this call.
func (s *configStorage) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	err := s.client.Do(ctx, s.client.B().Set().Key(key).Value(valkey.BinaryString(value)).Nx().Build()).Error()
	if err != nil {
		if err != valkey.Nil {
			return false, fmt.Errorf("failed storing key: %w", err)
		} else {
			return false, nil
		}
	}
	return true, nil
}

// Gets the value for the specified key.
func (s *configStorage) Get(ctx context.Context, key string) ([]byte, error) {
	ret, err := s.client.Do(ctx, s.client.B().Get().Key(key).Build()).AsBytes()
	if err == valkey.Nil {
		return nil, nil
	}
	return ret, err
}

func (s *configStorage) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	return s.getSetNxScript.Exec(ctx, s.client, []string{key}, []string{string(value)}).AsBytes()
}

func (s *configStorage) DeleteKeysForTemplateVersion(ctx context.Context, key string) error {
	prefix := fmt.Sprintf("%s*", key)
	v, err := s.client.Do(ctx, s.client.B().Scan().Cursor(0).Match(prefix).Build()).AsScanEntry()
	if err != nil {
		return fmt.Errorf("failed listing keys: %w", err)
	}
	err = s.client.Do(ctx, s.client.B().Del().Key(v.Elements...).Build()).Error()
	if err != nil {
		return fmt.Errorf("failed deleting keys: %w", err)
	}
	return nil
}

func (s *configStorage) PrintAllKeys(ctx context.Context) {
	v, err := s.client.Do(ctx, s.client.B().Scan().Cursor(0).Build()).AsScanEntry()
	if err != nil {
		fmt.Printf("failed listing keys: %v\n", err)
	}
	fmt.Printf("Keys: %v\n", v.Elements)
}
