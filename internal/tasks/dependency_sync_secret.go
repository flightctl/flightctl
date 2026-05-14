package tasks

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const secretSyncLabelPrefix = "flightctl.io/sync-"

// DependencySyncSecret manages a K8s Secret SharedInformer that detects
// changes to labeled secrets and emits DependencyChangeDetected events
// for affected fleets/devices.
type DependencySyncSecret struct {
	log               logrus.FieldLogger
	serviceHandler    service.Service
	releaseNamespace  string
	metrics           *DependencySyncCollector
	statusUpdater     *DependencySyncStatusUpdater
	informerConnected atomic.Bool
}

func NewDependencySyncSecret(log logrus.FieldLogger, serviceHandler service.Service, releaseNamespace string, metrics *DependencySyncCollector, statusUpdater *DependencySyncStatusUpdater) *DependencySyncSecret {
	return &DependencySyncSecret{
		log:              log,
		serviceHandler:   serviceHandler,
		releaseNamespace: releaseNamespace,
		metrics:          metrics,
		statusUpdater:    statusUpdater,
	}
}

func (d *DependencySyncSecret) IsInformerConnected() bool {
	return d.informerConnected.Load()
}

// Run starts the SharedInformerFactory watching labeled secrets and blocks
// until ctx is cancelled. Intended to be called as a goroutine.
func (d *DependencySyncSecret) Run(ctx context.Context, clientset kubernetes.Interface) {
	labelSelector := secretSyncLabelPrefix + d.releaseNamespace + "=true"
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = labelSelector
		}),
	)

	secretInformer := factory.Core().V1().Secrets().Informer()
	if _, err := secretInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			d.handleSecretEvent(ctx, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			d.handleSecretEvent(ctx, newObj)
		},
	}); err != nil {
		d.log.WithError(err).Error("Failed to add secret informer event handler")
		return
	}

	factory.Start(ctx.Done())
	synced := factory.WaitForCacheSync(ctx.Done())
	for typ, ok := range synced {
		if !ok {
			d.log.WithField("type", typ).Error("Informer failed to sync cache")
			d.setInformerDisconnected(ctx)
			return
		}
	}

	d.informerConnected.Store(true)
	if d.metrics != nil {
		d.metrics.SetInformerConnected(true)
	}
	d.log.Info("Secret informer cache synced, watching for changes")

	<-ctx.Done()
	d.setInformerDisconnected(ctx)
	d.log.Info("Secret informer stopped")
}

func (d *DependencySyncSecret) setInformerDisconnected(ctx context.Context) {
	d.informerConnected.Store(false)
	if d.metrics != nil {
		d.metrics.SetInformerConnected(false)
	}

	if d.statusUpdater == nil {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	orgIDs, st := d.serviceHandler.ListDistinctOrgIDsByRefType(bgCtx, "secret")
	if st.Code != http.StatusOK {
		d.log.Errorf("failed listing org IDs for disconnect propagation: %s", st.Message)
		return
	}
	for _, orgId := range orgIDs {
		d.statusUpdater.UpdateStatusForOrg(bgCtx, orgId, lo.ToPtr(false))
	}
}

func (d *DependencySyncSecret) handleSecretEvent(ctx context.Context, obj interface{}) {
	if ctx.Err() != nil {
		return
	}

	secret, ok := obj.(*corev1.Secret)
	if !ok {
		d.log.Warn("Received non-Secret object from informer")
		return
	}

	if d.metrics != nil {
		d.metrics.ObserveProbeCycle("secret")
	}

	d.reconcile(ctx, secret.Namespace, secret.Name, secret.ResourceVersion)
}

// reconcile queries for dependency targets whose stored fingerprint differs from
// newFingerprint (or has no fingerprint yet), emits DependencyChangeDetected
// events for changed targets, and updates sync_state.
func (d *DependencySyncSecret) reconcile(ctx context.Context, namespace, name, newFingerprint string) {
	refs, status := d.serviceHandler.ListSecretDependencyTargets(ctx, namespace, name, newFingerprint)
	if status.Code != http.StatusOK {
		d.log.Errorf("failed listing secret dependency targets for %s/%s: %s", namespace, name, status.Message)
		return
	}
	if len(refs) == 0 {
		return
	}

	if d.metrics != nil {
		d.metrics.ObserveProbeChange("secret")
	}

	resourceKey := fmt.Sprintf("secret:%s/%s", namespace, name)
	now := time.Now().UTC()

	orgIDs := make(map[uuid.UUID]bool)
	for _, ref := range refs {
		orgIDs[ref.OrgID] = true
		var kind domain.ResourceKind
		var targetName string
		if ref.DeviceName != "" {
			kind = domain.DeviceKind
			targetName = ref.DeviceName
		} else {
			kind = domain.FleetKind
			targetName = ref.FleetName
		}
		event := common.GetDependencyChangeDetectedEvent(ctx, kind, targetName, resourceKey, newFingerprint, "secret_informer")
		if event != nil {
			d.serviceHandler.CreateEvent(ctx, ref.OrgID, event)
		}
	}

	state := &model.SyncState{
		OrgID:         uuid.Nil,
		ResourceKey:   resourceKey,
		Fingerprint:   newFingerprint,
		ProbeStatus:   "Synced",
		LastCheckedAt: now,
		LastChangeAt:  &now,
	}
	if st := d.serviceHandler.SetSyncState(ctx, uuid.Nil, state); st.Code != http.StatusOK {
		d.log.Errorf("failed setting sync state for %s: %s", resourceKey, st.Message)
	}

	if d.statusUpdater != nil {
		for orgId := range orgIDs {
			d.statusUpdater.UpdateStatusForOrg(ctx, orgId, lo.ToPtr(true))
		}
	}
}
