package tpm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpm2"
	"gopkg.in/yaml.v3"
)

var (
	ErrNotFound = errors.New("not found")
)

// storageData represents the structure for persisted TPM data
type storageData struct {
	LDevID          *keyData                       `json:"ldevid,omitempty"`
	LAK             *keyData                       `json:"lak,omitempty"`
	SealedPassword  *passwordData                  `json:"sealed_password,omitempty"`
	ApplicationKeys map[string]*applicationKeyData `json:"app_keys,omitempty"`
}

// keyData represents persisted key information
type keyData struct {
	PublicBlob  string  `json:"public_blob"`
	PrivateBlob string  `json:"private_blob"`
	Password    *string `json:"password,omitempty"`
}

type applicationKeyData struct {
	KeyData        keyData `json:"key_data,omitempty"`
	ParentHandle   uint32  `json:"parent_handle"`
	ParentPassword *string `json:"parent_password,omitempty"`
}

// passwordData represents persisted password information
type passwordData struct {
	EncodedPassword string `json:"encoded_password"`
}

func (k *keyData) Public() (*tpm2.TPM2BPublic, error) {
	if k.PublicBlob == "" {
		return nil, fmt.Errorf("public blob is empty")
	}
	data, err := base64.StdEncoding.DecodeString(k.PublicBlob)
	if err != nil {
		return nil, fmt.Errorf("decode public key blob: %w", err)
	}

	public, err := tpm2.Unmarshal[tpm2.TPM2BPublic](data)
	if err != nil {
		return nil, fmt.Errorf("unmarshal public key as TPM2BPublic: %w", err)
	}
	return public, nil
}

func (k *keyData) Private() (*tpm2.TPM2BPrivate, error) {
	if k.PrivateBlob == "" {
		return nil, fmt.Errorf("private blob is empty")
	}
	data, err := base64.StdEncoding.DecodeString(k.PrivateBlob)
	if err != nil {
		return nil, fmt.Errorf("decode private key blob: %w", err)
	}

	private, err := tpm2.Unmarshal[tpm2.TPM2BPrivate](data)
	if err != nil {
		return nil, fmt.Errorf("unmarshal private key as TPM2BPrivate: %w", err)
	}
	return private, nil
}

func (k *keyData) Update(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) error {
	k.PublicBlob = base64.StdEncoding.EncodeToString(tpm2.Marshal(public))
	k.PrivateBlob = base64.StdEncoding.EncodeToString(tpm2.Marshal(private))
	return nil
}

func (k *applicationKeyData) Update(handle tpm2.TPMHandle, public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) error {
	if err := k.KeyData.Update(public, private); err != nil {
		return err
	}
	k.ParentHandle = handle.HandleValue()
	return nil
}

func (p *passwordData) Encoded() (string, error) {
	if p.EncodedPassword == "" {
		return "", fmt.Errorf("password is empty in storage")
	}
	return p.EncodedPassword, nil
}

func (p *passwordData) Clear() error {
	p.EncodedPassword = ""
	return nil
}

func (p *passwordData) Update(password []byte) {
	p.EncodedPassword = base64.StdEncoding.EncodeToString(password)
}

func (s *storageData) Handle(keyType KeyType) *keyData {
	switch keyType {
	case LDevID:
		return s.LDevID
	case LAK:
		return s.LAK
	default:
		return nil
	}
}

func (s *storageData) ClearHandle(keyType KeyType) error {
	switch keyType {
	case LDevID:
		s.LDevID = nil
	case LAK:
		s.LAK = nil
	default:
		return fmt.Errorf("invalid key type: %s", keyType)
	}
	return nil
}

func (s *storageData) Password() (*passwordData, error) {
	if s.SealedPassword == nil {
		return nil, fmt.Errorf("password %w", ErrNotFound)
	}
	return s.SealedPassword, nil
}

// fileStorage implements Storage interface for file-based persistence
type fileStorage struct {
	mu   sync.Mutex
	rw   fileio.ReadWriter
	path string
	log  *log.PrefixLogger
}

// NewFileStorage creates a new file-based storage implementation
func NewFileStorage(rw fileio.ReadWriter, path string, log *log.PrefixLogger) Storage {
	return &fileStorage{
		rw:   rw,
		path: path,
		log:  log,
	}
}

func (s *fileStorage) GetKey(keyType KeyType) (*tpm2.TPM2BPublic, *tpm2.TPM2BPrivate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return nil, nil, err
	}

	key := data.Handle(keyType)
	if key == nil {
		return nil, nil, fmt.Errorf("key %s %w", keyType, ErrNotFound)
	}

	public, err := key.Public()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load public key for %s from storage: %w", keyType, err)
	}
	private, err := key.Private()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load private key for %s from storage: %w", keyType, err)
	}

	s.log.Debugf("Successfully loaded key %s from storage", keyType)
	return public, private, nil
}

func (s *fileStorage) GetApplicationKey(appName string) (*AppKeyStoreData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return nil, err
	}

	key, ok := data.ApplicationKeys[appName]
	if !ok {
		return nil, fmt.Errorf("child key %s %w", appName, ErrNotFound)
	}

	public, err := key.KeyData.Public()
	if err != nil {
		return nil, fmt.Errorf("public key for %s from storage: %w", appName, err)
	}
	private, err := key.KeyData.Private()
	if err != nil {
		return nil, fmt.Errorf("private key for %s from storage: %w", appName, err)
	}

	res := &AppKeyStoreData{
		ParentHandle: tpm2.TPMHandle(key.ParentHandle),
		Public:       *public,
		Private:      *private,
	}
	if key.ParentPassword != nil {
		res.ParentPass, err = base64.StdEncoding.DecodeString(*key.ParentPassword)
		if err != nil {
			return nil, fmt.Errorf("decode parent password for %s from storage: %w", appName, err)
		}
	}
	if key.KeyData.Password != nil {
		res.Pass, err = base64.StdEncoding.DecodeString(*key.KeyData.Password)
		if err != nil {
			return nil, fmt.Errorf("decode password for %s from storage: %w", appName, err)
		}
	}
	s.log.Debugf("Successfully loaded app key %s from storage", appName)
	return res, nil
}

func (s *fileStorage) StoreKey(keyType KeyType, public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Debugf("Storing key %s to disk", keyType)

	data, err := s.readData()
	if err != nil {
		return fmt.Errorf("reading storage data: %w", err)
	}

	// ensure the key exists and update it
	if err := s.ensureKey(data, keyType); err != nil {
		return fmt.Errorf("ensuring key structure: %w", err)
	}

	if err := data.Handle(keyType).Update(public, private); err != nil {
		return fmt.Errorf("updating key data: %w", err)
	}

	if err := s.writeData(data); err != nil {
		return fmt.Errorf("writing key data to disk: %w", err)
	}

	// validate the key was stored correctly by attempting to read it back
	// Note: we need to re-read from disk to validate, but we already hold the lock
	data, err = s.readData()
	if err != nil {
		s.log.Errorf("Failed to re-read data for validation of key %s: %v", keyType, err)
		return fmt.Errorf("validation read failed for stored key %s: %w", keyType, err)
	}

	key := data.Handle(keyType)
	if key == nil {
		s.log.Errorf("Stored key %s not found during validation", keyType)
		return fmt.Errorf("stored key %s not found during validation", keyType)
	}

	storedPub, err := key.Public()
	if err != nil {
		s.log.Errorf("Failed to validate stored public key %s: %v", keyType, err)
		return fmt.Errorf("validation failed for stored public key %s: %w", keyType, err)
	}
	storedPriv, err := key.Private()
	if err != nil {
		s.log.Errorf("Failed to validate stored private key %s: %v", keyType, err)
		return fmt.Errorf("validation failed for stored private key %s: %w", keyType, err)
	}

	if storedPub == nil || storedPriv == nil {
		s.log.Errorf("Stored key %s appears to be corrupted (nil after storage)", keyType)
		return fmt.Errorf("stored key %s is corrupted", keyType)
	}

	s.log.Debugf("Successfully stored and validated key %s", keyType)
	return nil
}

func (s *fileStorage) StoreApplicationKey(appName string, keyData AppKeyStoreData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Debugf("Storing key %s to disk", appName)

	data, err := s.readData()
	if err != nil {
		return fmt.Errorf("reading storage data: %w", err)
	}

	if data.ApplicationKeys == nil {
		data.ApplicationKeys = make(map[string]*applicationKeyData)
	}

	entry, ok := data.ApplicationKeys[appName]
	if !ok {
		entry = &applicationKeyData{}
		data.ApplicationKeys[appName] = entry
	}

	// TODO passwords
	if err := entry.Update(keyData.ParentHandle, keyData.Public, keyData.Private); err != nil {
		return fmt.Errorf("updating key data: %s : %w", appName, err)
	}

	if err := s.writeData(data); err != nil {
		return fmt.Errorf("writing key data to disk: %w", err)
	}
	return nil
}

func (s *fileStorage) ClearKey(keyType KeyType) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return err
	}

	if err = data.ClearHandle(keyType); err != nil {
		return fmt.Errorf("clearing key %s: %w", keyType, err)
	}

	return s.writeData(data)
}

func (s *fileStorage) ClearApplicationKeys() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return err
	}
	data.ApplicationKeys = make(map[string]*applicationKeyData)

	return s.writeData(data)
}

func (s *fileStorage) ClearApplicationKey(appName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return err
	}
	delete(data.ApplicationKeys, appName)

	return s.writeData(data)
}

func (s *fileStorage) GetPassword() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return nil, err
	}

	password, err := data.Password()
	if err != nil {
		return nil, err
	}

	encodedPassword, err := password.Encoded()
	if err != nil {
		return nil, fmt.Errorf("reading encoded password: %w", err)
	}

	// decode base64 password to get raw bytes for TPM operations
	rawPassword, err := base64.StdEncoding.DecodeString(encodedPassword)
	if err != nil {
		return nil, fmt.Errorf("decoding base64 password: %w", err)
	}

	return rawPassword, nil
}

func (s *fileStorage) StorePassword(newPassword []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return err
	}

	// ensure password data exists
	if data.SealedPassword == nil {
		data.SealedPassword = &passwordData{}
	}

	currentPassword, err := data.Password()
	if err != nil {
		return err
	}

	currentPassword.Update(newPassword)
	return s.writeData(data)
}

func (s *fileStorage) ClearPassword() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.readData()
	if err != nil {
		return err
	}

	if data.SealedPassword != nil {
		password, err := data.Password()
		if err != nil {
			return fmt.Errorf("getting password: %w", err)
		}
		if err := password.Clear(); err != nil {
			return fmt.Errorf("clearing password in data: %w", err)
		}
	}

	return s.writeData(data)
}

func (s *fileStorage) Close() error {
	// no resources to close for file storage
	return nil
}

func (s *fileStorage) readData() (*storageData, error) {
	var data storageData

	fileData, err := s.rw.ReadFile(s.path)
	if err != nil {
		// if file does not exist return empty data
		if os.IsNotExist(err) {
			s.log.Infof("TPM file storage does not exist: initializing")
			return &data, nil
		}
		return nil, fmt.Errorf("reading tpm storage: %w", err)
	}

	err = yaml.Unmarshal(fileData, &data)
	if err != nil {
		return nil, fmt.Errorf("unmarshaling YAML from file %s: %w", s.path, err)
	}

	return &data, nil
}

func (s *fileStorage) writeData(data *storageData) error {
	fileData, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling TPM data to YAML: %w", err)
	}

	err = s.rw.WriteFile(s.path, fileData, 0600)
	if err != nil {
		return fmt.Errorf("writing TPM data to file %s: %w", s.path, err)
	}

	return nil
}

func (s *fileStorage) ensureKey(data *storageData, keyType KeyType) error {
	switch keyType {
	case LDevID:
		if data.LDevID == nil {
			data.LDevID = &keyData{}
		}
	case LAK:
		if data.LAK == nil {
			data.LAK = &keyData{}
		}
	default:
		return fmt.Errorf("unsupported key type: %s", keyType)
	}
	return nil
}
