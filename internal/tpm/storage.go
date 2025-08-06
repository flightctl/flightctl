package tpm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"

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
	LDevID         *keyData      `json:"ldevid,omitempty"`
	LAK            *keyData      `json:"lak,omitempty"`
	SealedPassword *passwordData `json:"sealed_password,omitempty"`
}

// keyData represents persisted key information
type keyData struct {
	PublicBlob  string `json:"public_blob"`
	PrivateBlob string `json:"private_blob"`
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

func (s *fileStorage) StoreKey(keyType KeyType, public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) error {
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
	storedPub, storedPriv, err := s.GetKey(keyType)
	if err != nil {
		s.log.Errorf("Failed to validate stored key %s: %v", keyType, err)
		return fmt.Errorf("validation failed for stored key %s: %w", keyType, err)
	}

	if storedPub == nil || storedPriv == nil {
		s.log.Errorf("Stored key %s appears to be corrupted (nil after storage)", keyType)
		return fmt.Errorf("stored key %s is corrupted", keyType)
	}

	s.log.Debugf("Successfully stored and validated key %s", keyType)
	return nil
}

func (s *fileStorage) ClearKey(keyType KeyType) error {
	data, err := s.readData()
	if err != nil {
		return err
	}

	if err = data.ClearHandle(keyType); err != nil {
		return fmt.Errorf("clearing key %s: %w", keyType, err)
	}

	return s.writeData(data)
}

func (s *fileStorage) GetPassword() ([]byte, error) {
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
