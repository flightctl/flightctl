package tasks

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"sort"
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

const secretSyncLabel = "flightctl.io/sync"

// DependencySyncSecret manages a K8s Secret SharedInformer that detects
// changes to labeled secrets and emits DependencyChangeDetected events
// for affected fleets/devices.
type DependencySyncSecret struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	hashFunc       func(data map[string][]byte) string
}

func NewDependencySyncSecret(log logrus.FieldLogger, serviceHandler service.Service) *DependencySyncSecret {
	return &DependencySyncSecret{
		log:            log,
		serviceHandler: serviceHandler,
		hashFunc:       hashSecretData,
	}
}

// Run starts the SharedInformerFactory watching labeled secrets and blocks
// until ctx is cancelled. Intended to be called as a goroutine.
func (d *DependencySyncSecret) Run(ctx context.Context, clientset kubernetes.Interface) {
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = secretSyncLabel + "=true"
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

	fingerprint := d.hashFunc(secret.Data)
	d.reconcile(ctx, secret.Namespace, secret.Name, fingerprint)
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

	orgsToUpdate := make(map[uuid.UUID]bool)
	for _, ref := range refs {
		firstSeen := ref.Fingerprint == nil

		if !firstSeen {
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

		orgsToUpdate[ref.OrgID] = true
	}

	for orgID := range orgsToUpdate {
		state := &model.SyncState{
			OrgID:         orgID,
			ResourceKey:   resourceKey,
			Fingerprint:   newFingerprint,
			LastCheckedAt: now,
			LastChangeAt:  &now,
		}
		if st := d.serviceHandler.SetSyncState(ctx, orgID, state); st.Code != http.StatusOK {
			d.log.Errorf("failed setting sync state for %s (org %s): %s", resourceKey, orgID, st.Message)
		}
	}
}

// hashSecretData computes a deterministic sha256 hash of a secret's .data field.
// Keys are sorted before hashing to ensure consistent output regardless of map
// iteration order.
func hashSecretData(data map[string][]byte) string {
	h := sha256.New()

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(data[k])
		h.Write([]byte{0})
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
