package tasks

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
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
	log              logrus.FieldLogger
	serviceHandler   service.Service
	releaseNamespace string
}

func NewDependencySyncSecret(log logrus.FieldLogger, serviceHandler service.Service, releaseNamespace string) *DependencySyncSecret {
	return &DependencySyncSecret{
		log:              log,
		serviceHandler:   serviceHandler,
		releaseNamespace: releaseNamespace,
	}
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
			return
		}
	}

	<-ctx.Done()
	d.log.Info("Secret informer stopped")
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

	resourceKey := fmt.Sprintf("secret:%s/%s", namespace, name)
	now := time.Now().UTC()

	for _, ref := range refs {
		var kind domain.ResourceKind
		var targetName string
		if ref.DeviceName != "" {
			kind = domain.DeviceKind
			targetName = ref.DeviceName
		} else {
			kind = domain.FleetKind
			targetName = ref.FleetName
		}
		event := common.GetDependencyChangeDetectedEvent(ctx, kind, targetName, resourceKey, newFingerprint)
		if event != nil {
			d.serviceHandler.CreateEvent(ctx, ref.OrgID, event)
		}
	}

	state := &model.SyncState{
		OrgID:         uuid.Nil,
		ResourceKey:   resourceKey,
		Fingerprint:   newFingerprint,
		LastCheckedAt: now,
		LastChangeAt:  &now,
	}
	if st := d.serviceHandler.SetSyncState(ctx, uuid.Nil, state); st.Code != http.StatusOK {
		d.log.Errorf("failed setting sync state for %s: %s", resourceKey, st.Message)
	}
}
