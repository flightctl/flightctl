package updatercatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	catalogstore "github.com/flightctl/flightctl/internal/store/updatercatalog"
	"gorm.io/gorm"
)

type PolicySnapshot struct {
	Revision    int64
	Digest      string
	Bindings    []ReleaseBinding
	Revocations []Revocation
}

type ActivationResult struct {
	Revision       int64
	Changed        bool
	DocumentDigest string
	Snapshot       PolicySnapshot
}

type Service struct{ store *catalogstore.Store }

func NewService(store *catalogstore.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("updater catalog requires a store")
	}
	return &Service{store: store}, nil
}

func Bootstrap(ctx context.Context, db *gorm.DB, path string) (*Service, error) {
	st, err := catalogstore.NewStore(db)
	if err != nil {
		return nil, err
	}
	service, err := NewService(st)
	if err != nil {
		return nil, err
	}
	if path != "" {
		candidate, err := ReadAdministratorConfig(path)
		if err != nil {
			return nil, err
		}
		if _, err = service.Activate(ctx, candidate); err != nil {
			return nil, fmt.Errorf("activate administrator updater catalog: %w", err)
		}
	}
	return service, nil
}

func ReadAdministratorConfig(path string) (AdministratorConfig, error) {
	file, err := os.Open(path) //nolint:gosec // Only administrator-owned configuration supplies this path.
	if err != nil {
		return AdministratorConfig{}, fmt.Errorf("open administrator updater catalog: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil {
		return AdministratorConfig{}, fmt.Errorf("read administrator updater catalog: %w", err)
	}
	return ParseAdministratorConfig(data)
}

func (s *Service) Activate(ctx context.Context, candidate AdministratorConfig) (ActivationResult, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "Activate")
	defer span.End()
	// Re-parse a copy so typed callers receive the same whole-config checks and
	// normalization without modifying their slices or access grants.
	data, err := json.Marshal(candidate)
	if err != nil {
		return ActivationResult{}, err
	}
	candidate, err = ParseAdministratorConfig(data)
	if err != nil {
		return ActivationResult{}, err
	}
	inputDigest, err := candidate.Digest()
	if err != nil {
		return ActivationResult{}, err
	}
	update := catalogstore.Candidate{Revision: candidate.Revision, InputDigest: inputDigest}
	for _, r := range candidate.Releases {
		data, err := canonicalJSON(r)
		if err != nil {
			return ActivationResult{}, err
		}
		record := catalogstore.Release{Name: r.Name, Version: r.Version, DescriptorDigest: digestBytes(data), Descriptor: data}
		evidence, err := r.RetainedEvidence()
		if err != nil {
			return ActivationResult{}, err
		}
		for _, e := range evidence {
			record.Schemas = append(record.Schemas, catalogstore.Schema{Digest: e.SchemaDigest, ObservationSchema: e.ObservationSchema, BudgetKeys: e.BudgetKeySet})
		}
		update.Releases = append(update.Releases, record)
	}
	for _, r := range candidate.Revocations {
		update.Revocations = append(update.Revocations, model.UpdaterRevocation{Name: r.Name, Version: r.Version, DescriptorDigest: r.DescriptorDigest, Reason: r.Reason})
	}
	result, err := s.store.Activate(ctx, update)
	if err != nil {
		return ActivationResult{}, err
	}
	snapshot, err := projectSnapshot(result.Snapshot)
	if err != nil {
		return ActivationResult{}, err
	}
	return ActivationResult{Revision: result.Snapshot.Revision, Changed: result.Changed, DocumentDigest: result.Snapshot.InputDigest, Snapshot: snapshot}, nil
}

func (s *Service) Snapshot(ctx context.Context) (PolicySnapshot, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "Snapshot")
	defer span.End()
	snapshot, err := s.store.Snapshot(ctx)
	if err != nil {
		return PolicySnapshot{}, unavailable(err)
	}
	if snapshot.Revision == 0 {
		return PolicySnapshot{}, common.ErrApprovedDescriptorsUnavailable
	}
	return projectSnapshot(snapshot)
}

func projectSnapshot(snapshot catalogstore.Snapshot) (PolicySnapshot, error) {
	result := PolicySnapshot{Revision: snapshot.Revision, Digest: snapshot.PolicyDigest, Bindings: []ReleaseBinding{}, Revocations: []Revocation{}}
	for _, record := range snapshot.Releases {
		binding, err := decodeBinding(record)
		if err != nil {
			return PolicySnapshot{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	for _, r := range snapshot.Tombstones {
		result.Revocations = append(result.Revocations, Revocation{Name: r.Name, Version: r.Version, DescriptorDigest: r.DescriptorDigest, Reason: r.Reason})
	}
	return result, nil
}

func decodeBinding(record model.UpdaterRelease) (ReleaseBinding, error) {
	if digestBytes(record.Descriptor) != record.DescriptorDigest {
		return ReleaseBinding{}, unavailable(errors.New("retained descriptor digest mismatch"))
	}
	var binding ReleaseBinding
	decoder := json.NewDecoder(bytes.NewReader(record.Descriptor))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return ReleaseBinding{}, unavailable(err)
	}
	if binding.Name != record.Name || binding.Version != record.Version {
		return ReleaseBinding{}, unavailable(errors.New("retained descriptor identity mismatch"))
	}
	return binding, nil
}

func (s *Service) LookupRelease(ctx context.Context, name, version string) (*common.ApprovedDescriptor, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "LookupRelease")
	defer span.End()
	result, err := s.store.LookupRelease(ctx, name, version)
	if err != nil {
		return nil, unavailable(err)
	}
	if result.Binding == nil {
		return nil, nil
	}
	binding, err := decodeBinding(*result.Binding)
	if err != nil {
		return nil, err
	}
	descriptor, err := binding.Descriptor()
	if err != nil {
		return nil, unavailable(err)
	}
	descriptor.Approved = result.Approved
	return descriptor, nil
}

func (s *Service) LookupSchemaByDigest(ctx context.Context, updater, schemaDigest string) (*common.RetainedSchemaEvidence, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/updatercatalog", "LookupSchemaByDigest")
	defer span.End()
	record, err := s.store.LookupSchemaByDigest(ctx, updater, schemaDigest)
	if err != nil {
		return nil, unavailable(err)
	}
	if record == nil {
		return nil, nil
	}
	return &common.RetainedSchemaEvidence{SchemaDigest: record.Digest, ObservationSchema: bytes.Clone(record.ObservationSchema), BudgetKeySet: slices.Clone(record.BudgetKeys)}, nil
}

func (r ReleaseBinding) Descriptor() (*common.ApprovedDescriptor, error) {
	data, err := canonicalJSON(r)
	if err != nil {
		return nil, err
	}
	descriptor := &common.ApprovedDescriptor{Name: r.Name, Version: r.Version, BuildDigest: r.PackageDigest, SchemaDigest: r.Schemas.Config.Digest, ConfigSchema: json.RawMessage(r.Schemas.Config.Bytes), ObservationSchemaDigest: r.Schemas.Observation.Digest, ObservationSchema: json.RawMessage(r.Schemas.Observation.Bytes), BudgetKeySet: slices.Clone(r.BudgetKeySet), GUIDPaths: slices.Clone(r.GUIDPaths), TemplatablePaths: slices.Clone(r.TemplatablePaths), DescriptorJSON: data}
	for _, ref := range r.ReferencePaths {
		descriptor.ReferencePaths = append(descriptor.ReferencePaths, common.FirmwareReferencePath{Path: ref.Path, Kind: ref.Kind})
	}
	return descriptor, nil
}

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", common.ErrApprovedDescriptorsUnavailable, err)
}

var _ common.ApprovedDescriptorProvider = (*Service)(nil)
