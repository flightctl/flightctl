package labelsyncmapping

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

// InitialLabelSyncMappingProvisioner applies a deployment-provided mapping sequence.
type InitialLabelSyncMappingProvisioner struct {
	filePath       string
	service        Service
	log            logrus.FieldLogger
	missingFileLog sync.Once
}

// NewInitialLabelSyncMappingProvisioner creates a best-effort seeder for deployment-provided mappings.
func NewInitialLabelSyncMappingProvisioner(filePath string, service Service, log logrus.FieldLogger) (*InitialLabelSyncMappingProvisioner, error) {
	if filePath == "" {
		return nil, errors.New("initial label-sync mappings file path is required")
	}
	if service == nil {
		return nil, errors.New("label-sync mapping service is required")
	}
	if log == nil {
		log = logrus.New()
	}
	return &InitialLabelSyncMappingProvisioner{filePath: filePath, service: service, log: log}, nil
}

// Provision applies the configured mappings to one organization.
func (p *InitialLabelSyncMappingProvisioner) Provision(ctx context.Context, orgID uuid.UUID) {
	p.ProvisionForOrganizations(ctx, []uuid.UUID{orgID})
}

// ProvisionForOrganizations reads the source once and attempts every mapping for each organization.
func (p *InitialLabelSyncMappingProvisioner) ProvisionForOrganizations(ctx context.Context, orgIDs []uuid.UUID) {
	entries, err := ReadInitialLabelSyncMappingEntries(p.filePath)
	if err != nil {
		p.logManifestError(err)
		return
	}
	p.ProvisionEntriesForOrganizations(ctx, orgIDs, entries)
}

// ReadInitialLabelSyncMappingEntries reads and validates the manifest's top-level YAML sequence.
// Individual entries are decoded during provisioning so a malformed entry does not block later ones.
func ReadInitialLabelSyncMappingEntries(filePath string) ([]json.RawMessage, error) {
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading initial label-sync mappings: %w", err)
	}

	var entries []json.RawMessage
	if err := yaml.Unmarshal(contents, &entries); err != nil {
		return nil, fmt.Errorf("parsing initial label-sync mappings: %w", err)
	}
	if entries == nil {
		return nil, errors.New("initial label-sync mappings must be a YAML sequence")
	}
	return entries, nil
}

// ProvisionEntriesForOrganizations attempts each decoded manifest entry for each organization.
func (p *InitialLabelSyncMappingProvisioner) ProvisionEntriesForOrganizations(ctx context.Context, orgIDs []uuid.UUID, entries []json.RawMessage) {
	if len(entries) == 0 {
		return
	}

	for _, orgID := range orgIDs {
		for entryIndex, entry := range entries {
			var mapping domain.LabelSyncMapping
			if err := json.Unmarshal(entry, &mapping); err != nil {
				p.log.WithError(err).WithFields(logrus.Fields{
					"file":        p.filePath,
					"org_id":      orgID,
					"entry_index": entryIndex,
				}).Error("Failed to decode initial label-sync mapping")
				continue
			}

			_, status := CreateLabelSyncMappingFromInitialManifest(ctx, p.service, orgID, mapping)
			if err := common.ApiStatusToErr(status); err != nil {
				logEntry := p.log.WithError(err).WithFields(logrus.Fields{
					"file":        p.filePath,
					"org_id":      orgID,
					"mapping":     mappingName(mapping),
					"entry_index": entryIndex,
				})
				if status.Code == http.StatusConflict {
					logEntry.Info("Initial label-sync mapping already exists or conflicts")
					continue
				}
				logEntry.Error("Failed to create initial label-sync mapping")
			}
		}
	}
}

func (p *InitialLabelSyncMappingProvisioner) logManifestError(err error) {
	logEntry := p.log.WithError(err).WithField("file", p.filePath)
	if errors.Is(err, os.ErrNotExist) {
		p.missingFileLog.Do(func() {
			logEntry.Warn("Initial label-sync mappings file is not available")
		})
		return
	}
	logEntry.Error("Failed to load initial label-sync mappings")
}

func mappingName(mapping domain.LabelSyncMapping) string {
	if mapping.Metadata.Name == nil {
		return ""
	}
	return *mapping.Metadata.Name
}
