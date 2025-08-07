package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	NullOrgId              = org.DefaultID
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
	Checkpoint() Checkpoint
	Organization() Organization
	RunMigrations(context.Context) error
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
	checkpoint                Checkpoint
	organization              Organization

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
		checkpoint:                NewCheckpoint(db, log),
		organization:              NewOrganization(db),
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

func (s *DataStore) Checkpoint() Checkpoint {
	return s.checkpoint
}

func (s *DataStore) Organization() Organization {
	return s.organization
}

func (s *DataStore) RunMigrationWithMigrationUser(ctx context.Context, cfg *config.Config, log *logrus.Logger) error {
	ctx, span := instrumentation.StartSpan(ctx, "flightctl/store", "RunMigrationWithMigrationUser")
	defer span.End()

	// Create migration database connection
	migrationDB, err := InitMigrationDB(cfg, log)
	if err != nil {
		return fmt.Errorf("failed to create migration database connection: %w", err)
	}
	defer func() {
		if sqlDB, err := migrationDB.DB(); err == nil {
			sqlDB.Close()
		}
	}()

	// Create migration store with migration user
	migrationStore := NewStore(migrationDB, log.WithField("pkg", "migration-store"))
	defer migrationStore.Close()

	// Run migrations with migration user
	if err := migrationStore.RunMigrations(ctx); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Info("Database migration completed successfully")
	return nil
}

func (s *DataStore) RunMigrations(ctx context.Context) error {
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
	if err := s.Checkpoint().InitialMigration(ctx); err != nil {
		return err
	}
	if err := s.Organization().InitialMigration(ctx); err != nil {
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

type SortColumn string
type SortOrder string

const (
	SortByName      SortColumn = "name"
	SortByCreatedAt SortColumn = "created_at"

	SortAsc  SortOrder = "asc"
	SortDesc SortOrder = "desc"
)

type ListParams struct {
	Limit              int
	Continue           *Continue
	FieldSelector      *selector.FieldSelector
	LabelSelector      *selector.LabelSelector
	AnnotationSelector *selector.AnnotationSelector
	SortOrder          *SortOrder
	SortColumns        []SortColumn
}

type Continue struct {
	Version int
	Names   []string
	Count   int64
}

func BuildContinueString(names []string, count int64) *string {
	cont := Continue{
		Version: CurrentContinueVersion,
		Names:   names,
		Count:   count,
	}

	sEnc, _ := json.Marshal(cont)
	sEncStr := b64.StdEncoding.EncodeToString(sEnc)
	return &sEncStr
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
