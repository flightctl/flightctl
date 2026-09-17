package device

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

type Service interface {
	CreateDevice(ctx context.Context, orgId uuid.UUID, device domain.Device) (*domain.Device, domain.Status)
	ListDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*domain.DeviceList, domain.Status)
	ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*domain.DeviceList, domain.Status)
	UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error)
	GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status)
	ReplaceDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool, enforceCapabilities bool) (*domain.Device, domain.Status)
	// ReplaceDeviceSpec overwrites Spec and merges the given annotation changes for an
	// internal reconciler that already owns this device (fleet rollout). It does not
	// require the caller's resourceVersion to match, and never touches Labels or Owner,
	// so it cannot race with agent status heartbeats or clobber concurrent label/owner
	// writes. expectedOwner is checked against the freshly loaded owner on every retry
	// attempt; a mismatch returns a conflict so callers can detect it exactly as they
	// would a resourceVersion conflict today.
	ReplaceDeviceSpec(ctx context.Context, orgId uuid.UUID, name string, expectedOwner *string, spec domain.DeviceSpec, setAnnotations map[string]string, deleteAnnotations []string) (*domain.Device, domain.Status)
	// SetDeviceOwner sets (or clears, when newOwner is nil) only the Owner field.
	// Like ReplaceDeviceSpec, it skips resourceVersion matching and never touches
	// Spec/Labels/Annotations, so fleet-selector reassignment can't race with heartbeats
	// or silently overwrite a concurrently-rendered spec.
	SetDeviceOwner(ctx context.Context, orgId uuid.UUID, name string, expectedOwner *string, newOwner *string) (*domain.Device, domain.Status)
	DeleteDevice(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	GetDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status)
	GetDeviceLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceLastSeen, domain.Status)
	ReplaceDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, refreshLastSeen bool) (*domain.Device, domain.Status)
	PatchDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Device, domain.Status)
	GetRenderedDevice(ctx context.Context, orgId uuid.UUID, name string, params domain.GetRenderedDeviceParams) (*domain.Device, domain.Status)
	PatchDevice(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool, enforceCapabilities bool) (*domain.Device, domain.Status)
	DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom domain.DeviceDecommission) (*domain.Device, domain.Status)
	ResumeDevices(ctx context.Context, orgId uuid.UUID, request domain.DeviceResumeRequest) (domain.DeviceResumeResponse, domain.Status)
	UpdateDeviceAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status
	StopDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status)
	StartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status)
	RestartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status)
	UpdateRenderedDevice(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash, osImage string, configFingerprints []domain.DependencySyncConfigRefStatus, forceUpdate bool) domain.Status
	SetDeviceServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status
	OverwriteDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status
	GetDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status)
	CountDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, domain.Status)
	UnmarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status
	MarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) domain.Status
	GetDeviceCompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]domain.DeviceCompletionCount, domain.Status)
	CountDevicesByLabels(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, domain.Status)
	GetDevicesSummary(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*domain.DevicesSummary, domain.Status)
	UpdateServiceSideDeviceStatus(ctx context.Context, orgId uuid.UUID, device domain.Device) bool
	SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error
	UpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error
	ForceUpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error
	ListConnectivityChangedDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, cutoffTime time.Time) (*domain.DeviceList, domain.Status)
	ListLabels(ctx context.Context, orgId uuid.UUID, params domain.ListLabelsParams) (*domain.LabelList, domain.Status)
	// HealthcheckDevices records device heartbeats for the given names (batched by the healthchecker).
	HealthcheckDevices(ctx context.Context, orgId uuid.UUID, names []string) error
}
