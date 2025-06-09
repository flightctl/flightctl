package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/instrumentation"
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
	InitialMigration(context.Context) error
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

func (s *DataStore) InitialMigration(ctx context.Context) error {
	ctx, span := instrumentation.StartSpan(ctx, "flightctl/store", "InitialMigration")
	defer span.End()

	if err := s.Device().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.EnrollmentRequest().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.CertificateSigningRequest().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.Fleet().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.TemplateVersion().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.Repository().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.ResourceSync().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.Event().InitialMigration(ctx); err != nil {
		return err
	}
	return s.customizeMigration(ctx)
}

func (s *DataStore) customizeMigration(ctx context.Context) error {
	db := s.db.WithContext(ctx)

	if db.Migrator().HasConstraint("fleet_repos", "fk_fleet_repos_repository") {
		if err := db.Migrator().DropConstraint("fleet_repos", "fk_fleet_repos_repository"); err != nil {
			return err
		}
	}
	if db.Migrator().HasConstraint("device_repos", "fk_device_repos_repository") {
		if err := db.Migrator().DropConstraint("device_repos", "fk_device_repos_repository"); err != nil {
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
