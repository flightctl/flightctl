package store

import (
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	NullOrgId              = uuid.MustParse("00000000-0000-0000-0000-000000000000")
	CurrentContinueVersion = 1
)

type Store interface {
	Device() Device
	EnrollmentRequest() EnrollmentRequest
	CertificateSigningRequest() CertificateSigningRequest
	Fleet() Fleet
	TemplateVersion() TemplateVersion
	Repository() Repository
	ResourceSync() ResourceSync
	Event() Event
	InitialMigration() error
	Close() error
}

type DataStore struct {
	device                    Device
	enrollmentRequest         EnrollmentRequest
	certificateSigningRequest CertificateSigningRequest
	fleet                     Fleet
	templateVersion           TemplateVersion
	repository                Repository
	resourceSync              ResourceSync
	event                     Event

	db *gorm.DB
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &DataStore{
		device:                    NewDevice(db, log),
		enrollmentRequest:         NewEnrollmentRequest(db, log),
		certificateSigningRequest: NewCertificateSigningRequest(db, log),
		fleet:                     NewFleet(db, log),
		templateVersion:           NewTemplateVersion(db, log),
		repository:                NewRepository(db, log),
		resourceSync:              NewResourceSync(db, log),
		event:                     NewEvent(db, log),
		db:                        db,
	}
}

func (s *DataStore) Repository() Repository {
	return s.repository
}

func (s *DataStore) Device() Device {
	return s.device
}

func (s *DataStore) EnrollmentRequest() EnrollmentRequest {
	return s.enrollmentRequest
}

func (s *DataStore) CertificateSigningRequest() CertificateSigningRequest {
	return s.certificateSigningRequest
}

func (s *DataStore) Fleet() Fleet {
	return s.fleet
}

func (s *DataStore) TemplateVersion() TemplateVersion {
	return s.templateVersion
}

func (s *DataStore) ResourceSync() ResourceSync {
	return s.resourceSync
}

func (s *DataStore) Event() Event {
	return s.event
}

func (s *DataStore) InitialMigration() error {
	if err := s.Device().InitialMigration(); err != nil {
		return err
	}
	if err := s.EnrollmentRequest().InitialMigration(); err != nil {
		return err
	}
	if err := s.CertificateSigningRequest().InitialMigration(); err != nil {
		return err
	}
	if err := s.Fleet().InitialMigration(); err != nil {
		return err
	}
	if err := s.TemplateVersion().InitialMigration(); err != nil {
		return err
	}
	if err := s.Repository().InitialMigration(); err != nil {
		return err
	}
	if err := s.ResourceSync().InitialMigration(); err != nil {
		return err
	}
	if err := s.Event().InitialMigration(); err != nil {
		return err
	}
	return s.customizeMigration()
}

func (s *DataStore) customizeMigration() error {
	if s.db.Migrator().HasConstraint("fleet_repos", "fk_fleet_repos_repository") {
		if err := s.db.Migrator().DropConstraint("fleet_repos", "fk_fleet_repos_repository"); err != nil {
			return err
		}
	}
	if s.db.Migrator().HasConstraint("device_repos", "fk_device_repos_repository") {
		if err := s.db.Migrator().DropConstraint("device_repos", "fk_device_repos_repository"); err != nil {
			return err
		}
	}
	return nil
}

func (s *DataStore) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

type ListParams struct {
	Limit              int
	Continue           *Continue
	FieldSelector      *selector.FieldSelector
	LabelSelector      *selector.LabelSelector
	AnnotationSelector *selector.AnnotationSelector
}

type Continue struct {
	Version int
	Name    string
	Count   int64
}

func ParseContinueString(contStr *string) (*Continue, error) {
	var cont Continue

	if contStr == nil {
		return nil, nil
	}

	sDec, err := b64.StdEncoding.DecodeString(*contStr)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(sDec, &cont); err != nil {
		return nil, err
	}
	if cont.Version != CurrentContinueVersion {
		return nil, fmt.Errorf("continue string version %d must be %d", cont.Version, CurrentContinueVersion)
	}

	return &cont, nil
}
