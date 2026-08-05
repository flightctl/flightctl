package migration

import (
	"context"
	"fmt"
	"strings"
	"time"

	v1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/service/common"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	migrateCatalogItemLabelsMigrationKey = "migrate_catalog_item_labels_v1"

	catalogLabelInfix = "catalog.flightctl.io/"

	osLabelCatalog = "os.catalog.flightctl.io/catalog"
	osLabelItem    = "os.catalog.flightctl.io/item"
	osLabelChannel = "os.catalog.flightctl.io/channel"

	appLabelSuffixCatalog = ".app.catalog.flightctl.io/catalog"
	appLabelSuffixItem    = ".app.catalog.flightctl.io/item"
	appLabelSuffixChannel = ".app.catalog.flightctl.io/channel"

	volumeLabelSuffixCatalog = ".volume.catalog.flightctl.io/catalog"
	volumeLabelSuffixItem    = ".volume.catalog.flightctl.io/item"
	volumeLabelSuffixChannel = ".volume.catalog.flightctl.io/channel"
)

type catalogItemCacheKey struct {
	orgID       uuid.UUID
	catalogName string
	itemName    string
}

func migrateCatalogItemLabels(ctx context.Context, tx *gorm.DB, log logrus.FieldLogger) error {
	return tx.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.SchemaMigration{Key: migrateCatalogItemLabelsMigrationKey, AppliedAt: time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		cache, err := buildCatalogItemCache(tx)
		if err != nil {
			return fmt.Errorf("build catalog item cache: %w", err)
		}

		catStore := catalogstore.NewCatalogStore(tx, log)

		if err := migrateDevices(ctx, tx, cache, catStore, log); err != nil {
			return fmt.Errorf("migrate devices: %w", err)
		}
		return migrateFleets(ctx, tx, cache, catStore, log)
	})
}

func buildCatalogItemCache(tx *gorm.DB) (map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec, error) {
	var items []model.CatalogItem
	if err := tx.Find(&items).Error; err != nil {
		return nil, err
	}
	cache := make(map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec, len(items))
	for i := range items {
		if items[i].Spec == nil {
			continue
		}
		key := catalogItemCacheKey{
			orgID:       items[i].OrgID,
			catalogName: items[i].CatalogName,
			itemName:    items[i].AppName,
		}
		spec := items[i].Spec.Data
		cache[key] = &spec
	}
	return cache, nil
}

func migrateDevices(ctx context.Context, tx *gorm.DB, cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec, catStore catalogstore.Store, log logrus.FieldLogger) error {
	type deviceRef struct {
		OrgID      uuid.UUID
		DeviceName string
	}
	var refs []deviceRef
	if err := tx.Raw(
		"SELECT DISTINCT org_id, device_name FROM device_labels WHERE label_key LIKE ?",
		"%"+catalogLabelInfix+"%",
	).Scan(&refs).Error; err != nil {
		return fmt.Errorf("query device_labels: %w", err)
	}

	if len(refs) == 0 {
		return nil
	}

	log.Infof("migrating catalog item labels on %d device(s)", len(refs))

	for _, ref := range refs {
		var device model.Device
		if err := tx.Where("org_id = ? AND name = ?", ref.OrgID, ref.DeviceName).First(&device).Error; err != nil {
			log.Warnf("device %s/%s not found, skipping: %v", ref.OrgID, ref.DeviceName, err)
			continue
		}

		labels := device.Labels
		if labels == nil {
			continue
		}

		if !hasCatalogLabels(labels) {
			continue
		}

		spec := device.Spec
		if spec == nil {
			removeCatalogLabels(labels)
			if err := tx.Model(&model.Device{}).
				Where("org_id = ? AND name = ?", ref.OrgID, ref.DeviceName).
				Update("labels", labels).Error; err != nil {
				return fmt.Errorf("update device %s/%s labels: %w", ref.OrgID, ref.DeviceName, err)
			}
			continue
		}

		deviceLog := log.WithField("device", ref.DeviceName)
		changed := migrateDeviceSpec(&spec.Data, labels, ref.OrgID, cache, deviceLog)

		if changed {
			if status := common.ValidateCatalogItemRefs(ctx, ref.OrgID, catStore, &spec.Data); status != v1beta1.StatusOK() {
				deviceLog.Warnf("migrated spec failed validation, skipping spec update: %s", status.Message)
				changed = false
			}
		}

		if !changed {
			continue
		}

		removeCatalogLabels(labels)

		if err := tx.Model(&model.Device{}).
			Where("org_id = ? AND name = ?", ref.OrgID, ref.DeviceName).
			Updates(map[string]any{
				"labels": labels,
				"spec":   spec,
			}).Error; err != nil {
			return fmt.Errorf("update device %s/%s: %w", ref.OrgID, ref.DeviceName, err)
		}
	}
	return nil
}

func migrateFleets(ctx context.Context, tx *gorm.DB, cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec, catStore catalogstore.Store, log logrus.FieldLogger) error {
	var fleets []model.Fleet
	if err := tx.Where("labels::text LIKE ?", "%"+catalogLabelInfix+"%").Find(&fleets).Error; err != nil {
		return fmt.Errorf("query fleets: %w", err)
	}

	if len(fleets) == 0 {
		return nil
	}

	log.Infof("migrating catalog item labels on %d fleet(s)", len(fleets))

	for i := range fleets {
		fleet := &fleets[i]
		labels := fleet.Labels
		if labels == nil || !hasCatalogLabels(labels) {
			continue
		}

		fleetLog := log.WithField("fleet", fleet.Name)
		var changed bool
		if fleet.Spec != nil {
			deviceSpec := &fleet.Spec.Data.Template.Spec
			changed = migrateDeviceSpec(deviceSpec, labels, fleet.OrgID, cache, fleetLog)
		}
		if changed {
			if status := common.ValidateCatalogItemRefs(ctx, fleet.OrgID, catStore, &fleet.Spec.Data.Template.Spec); status != v1beta1.StatusOK() {
				fleetLog.Warnf("migrated spec failed validation, skipping spec update: %s", status.Message)
				changed = false
			}
		}

		if !changed {
			continue
		}

		removeCatalogLabels(labels)

		if err := tx.Model(&model.Fleet{}).
			Where("org_id = ? AND name = ?", fleet.OrgID, fleet.Name).
			Updates(map[string]any{
				"labels": labels,
				"spec":   fleet.Spec,
			}).Error; err != nil {
			return fmt.Errorf("update fleet %s/%s: %w", fleet.OrgID, fleet.Name, err)
		}
	}
	return nil
}

// migrateDeviceSpec migrates catalog item labels to catalogItemRef fields on the
// device spec. Returns true if the spec was modified.
func migrateDeviceSpec(
	spec *v1beta1.DeviceSpec,
	labels map[string]string,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	changed := migrateOsSpec(spec, labels, orgID, cache, log)
	if migrateAppSpecs(spec, labels, orgID, cache, log) {
		changed = true
	}
	return changed
}

func migrateOsSpec(
	spec *v1beta1.DeviceSpec,
	labels map[string]string,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	catalogName, hasCatalog := labels[osLabelCatalog]
	itemName, hasItem := labels[osLabelItem]
	if !hasCatalog || !hasItem {
		return false
	}

	if spec.Os == nil || spec.Os.CatalogItemRef != nil {
		return false
	}

	if spec.Os.Image == "" {
		return false
	}

	key := catalogItemCacheKey{orgID: orgID, catalogName: catalogName, itemName: itemName}
	itemSpec, ok := cache[key]
	if !ok {
		log.Warnf("catalog item %s/%s not found, skipping OS migration", catalogName, itemName)
		return false
	}

	version := resolveVersionFromImage(itemSpec, spec.Os.Image)
	if version == "" {
		log.Warnf("no version matched for OS image %q in catalog item %s/%s", spec.Os.Image, catalogName, itemName)
		return false
	}

	channel := labels[osLabelChannel]
	spec.Os.CatalogItemRef = &v1beta1.CatalogItemRefSpec{
		Catalog: catalogName,
		Item:    itemName,
		Version: version,
		Channel: lo.ToPtr(channel),
	}
	spec.Os.Image = ""

	log.Infof("migrated OS to catalogItemRef: %s/%s@%s", catalogName, itemName, version)
	return true
}

func migrateAppSpecs(
	spec *v1beta1.DeviceSpec,
	labels map[string]string,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	if spec.Applications == nil {
		return false
	}

	appCatalogRefs := extractAppCatalogLabels(labels)

	apps := *spec.Applications
	changed := false
	for i := range apps {
		app := &apps[i]
		appType, err := app.GetAppType()
		if err != nil {
			continue
		}
		appName, err := app.GetName()
		if err != nil || appName == nil {
			continue
		}

		appRef := appCatalogRefs[*appName]
		if migrateAppWithVolumes(app, appType, *appName, appRef, labels, orgID, cache, log) {
			changed = true
		}
	}
	return changed
}

type catalogRef struct {
	catalog string
	item    string
	channel string
}

func extractAppCatalogLabels(labels map[string]string) map[string]catalogRef {
	refs := make(map[string]catalogRef)
	for key, value := range labels {
		if !strings.HasSuffix(key, appLabelSuffixCatalog) {
			continue
		}
		appName := strings.TrimSuffix(key, appLabelSuffixCatalog)
		if appName == "" {
			continue
		}
		ref := catalogRef{
			catalog: value,
			item:    labels[appName+appLabelSuffixItem],
			channel: labels[appName+appLabelSuffixChannel],
		}
		if ref.item != "" {
			refs[appName] = ref
		}
	}
	return refs
}

// migrateAppWithVolumes migrates a single application's image and its volumes'
// images from label-based catalog refs to catalogItemRef spec fields. It does a
// single concrete-type read/write cycle to handle both in one pass.
func migrateAppWithVolumes(
	app *v1beta1.ApplicationProviderSpec,
	appType v1beta1.AppType,
	appName string,
	appRef catalogRef,
	labels map[string]string,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	switch appType {
	case v1beta1.AppTypeContainer:
		a, err := app.AsContainerApplication()
		if err != nil {
			return false
		}
		appChanged := migrateAppImage(&a, appRef, orgID, cache, log)
		volChanged := migrateVolumes(a.Volumes, appName, labels, orgID, cache, log)
		if appChanged || volChanged {
			if err := app.FromContainerApplication(a); err != nil {
				log.Warnf("failed to write back container app: %v", err)
				return false
			}
			return true
		}
	case v1beta1.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return false
		}
		appChanged := migrateAppImage(&a, appRef, orgID, cache, log)
		volChanged := migrateVolumes(a.Volumes, appName, labels, orgID, cache, log)
		if appChanged || volChanged {
			if err := app.FromComposeApplication(a); err != nil {
				log.Warnf("failed to write back compose app: %v", err)
				return false
			}
			return true
		}
	case v1beta1.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return false
		}
		appChanged := migrateAppImage(&a, appRef, orgID, cache, log)
		volChanged := migrateVolumes(a.Volumes, appName, labels, orgID, cache, log)
		if appChanged || volChanged {
			if err := app.FromQuadletApplication(a); err != nil {
				log.Warnf("failed to write back quadlet app: %v", err)
				return false
			}
			return true
		}
	case v1beta1.AppTypeHelm:
		a, err := app.AsHelmApplication()
		if err != nil {
			return false
		}
		if appRef.catalog == "" {
			return false
		}
		appChanged := migrateAppImage(&a, appRef, orgID, cache, log)
		if appChanged {
			if err := app.FromHelmApplication(a); err != nil {
				log.Warnf("failed to write back helm app: %v", err)
				return false
			}
			return true
		}
	}
	return false
}

// appWithImage is implemented by all concrete app types that support
// image-based and catalog-item-ref-based providers.
type appWithImage interface {
	Type() v1beta1.ApplicationProviderType
	AsImageApplicationProviderSpec() (v1beta1.ImageApplicationProviderSpec, error)
	FromCatalogItemRefApplicationProviderSpec(v1beta1.CatalogItemRefApplicationProviderSpec) error
}

func migrateAppImage(
	app appWithImage,
	ref catalogRef,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	if ref.catalog == "" || ref.item == "" {
		return false
	}
	if app.Type() != v1beta1.ImageApplicationProviderType {
		return false
	}

	img, err := app.AsImageApplicationProviderSpec()
	if err != nil || img.Image == "" {
		return false
	}

	key := catalogItemCacheKey{orgID: orgID, catalogName: ref.catalog, itemName: ref.item}
	itemSpec, ok := cache[key]
	if !ok {
		log.Warnf("catalog item %s/%s not found, skipping app migration", ref.catalog, ref.item)
		return false
	}

	version := resolveVersionFromImage(itemSpec, img.Image)
	if version == "" {
		log.Warnf("no version matched for app image %q in catalog item %s/%s", img.Image, ref.catalog, ref.item)
		return false
	}

	catalogItemRef := v1beta1.CatalogItemRefApplicationProviderSpec{
		CatalogItemRef: v1beta1.CatalogItemRefSpec{
			Catalog: ref.catalog,
			Item:    ref.item,
			Version: version,
			Channel: lo.ToPtr(ref.channel),
		},
	}
	if err := app.FromCatalogItemRefApplicationProviderSpec(catalogItemRef); err != nil {
		log.Warnf("failed to set catalogItemRef on app: %v", err)
		return false
	}

	log.Infof("migrated app to catalogItemRef: %s/%s@%s", ref.catalog, ref.item, version)
	return true
}

func migrateVolumes(
	volumes *[]v1beta1.ApplicationVolume,
	appName string,
	labels map[string]string,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	if volumes == nil {
		return false
	}

	changed := false
	for i := range *volumes {
		vol := &(*volumes)[i]
		ref := lookupVolumeCatalogLabel(labels, appName, vol.Name)
		if ref.catalog == "" || ref.item == "" {
			continue
		}
		if migrateVolume(vol, ref, orgID, cache, log) {
			changed = true
		}
	}
	return changed
}

func lookupVolumeCatalogLabel(labels map[string]string, appName, volumeName string) catalogRef {
	prefix := appName + "." + volumeName
	catalog := labels[prefix+volumeLabelSuffixCatalog]
	if catalog == "" {
		return catalogRef{}
	}
	return catalogRef{
		catalog: catalog,
		item:    labels[prefix+volumeLabelSuffixItem],
		channel: labels[prefix+volumeLabelSuffixChannel],
	}
}

func migrateVolume(
	vol *v1beta1.ApplicationVolume,
	ref catalogRef,
	orgID uuid.UUID,
	cache map[catalogItemCacheKey]*v1alpha1.CatalogItemSpec,
	log logrus.FieldLogger,
) bool {
	volType, err := vol.Type()
	if err != nil {
		return false
	}

	key := catalogItemCacheKey{orgID: orgID, catalogName: ref.catalog, itemName: ref.item}
	itemSpec, ok := cache[key]
	if !ok {
		log.Warnf("catalog item %s/%s not found, skipping volume %s migration", ref.catalog, ref.item, vol.Name)
		return false
	}

	switch volType {
	case v1beta1.ImageApplicationVolumeProviderType:
		provider, err := vol.AsImageVolumeProviderSpec()
		if err != nil || provider.Image.CatalogItemRef != nil {
			return false
		}
		if provider.Image.Reference == "" {
			return false
		}
		version := resolveVersionFromImage(itemSpec, provider.Image.Reference)
		if version == "" {
			log.Warnf("no version matched for volume %s image %q in catalog item %s/%s", vol.Name, provider.Image.Reference, ref.catalog, ref.item)
			return false
		}
		provider.Image.CatalogItemRef = &v1beta1.CatalogItemRefSpec{
			Catalog: ref.catalog,
			Item:    ref.item,
			Version: version,
			Channel: lo.ToPtr(ref.channel),
		}
		provider.Image.Reference = ""
		if err := vol.FromImageVolumeProviderSpec(provider); err != nil {
			log.Warnf("failed to write back volume %s: %v", vol.Name, err)
			return false
		}

	case v1beta1.ImageMountApplicationVolumeProviderType:
		provider, err := vol.AsImageMountVolumeProviderSpec()
		if err != nil || provider.Image.CatalogItemRef != nil {
			return false
		}
		if provider.Image.Reference == "" {
			return false
		}
		version := resolveVersionFromImage(itemSpec, provider.Image.Reference)
		if version == "" {
			log.Warnf("no version matched for volume %s image %q in catalog item %s/%s", vol.Name, provider.Image.Reference, ref.catalog, ref.item)
			return false
		}
		provider.Image.CatalogItemRef = &v1beta1.CatalogItemRefSpec{
			Catalog: ref.catalog,
			Item:    ref.item,
			Version: version,
			Channel: lo.ToPtr(ref.channel),
		}
		provider.Image.Reference = ""
		if err := vol.FromImageMountVolumeProviderSpec(provider); err != nil {
			log.Warnf("failed to write back volume %s: %v", vol.Name, err)
			return false
		}

	default:
		return false
	}

	log.Infof("migrated volume %s to catalogItemRef: %s/%s", vol.Name, ref.catalog, ref.item)
	return true
}

func resolveVersionFromImage(itemSpec *v1alpha1.CatalogItemSpec, image string) string {
	artifact := itemSpec.FindArtifact(v1alpha1.CatalogItemArtifactTypeContainer)
	if artifact == nil {
		return ""
	}
	for _, version := range itemSpec.Versions {
		tag := version.References[v1alpha1.CatalogItemArtifactTypeContainer]
		if tag == "" {
			continue
		}
		sep := ":"
		if strings.Contains(tag, ":") {
			sep = "@"
		}
		if artifact.Uri+sep+tag == image {
			return version.Version
		}
	}
	return ""
}

func hasCatalogLabels(labels map[string]string) bool {
	for key := range labels {
		if strings.Contains(key, catalogLabelInfix) {
			return true
		}
	}
	return false
}

func removeCatalogLabels(labels map[string]string) {
	for key := range labels {
		if strings.Contains(key, catalogLabelInfix) {
			delete(labels, key)
		}
	}
}
