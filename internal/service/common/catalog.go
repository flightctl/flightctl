package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// CollectCatalogItemRefs extracts all CatalogItemRefSpec values from a device spec's
// OS, application, and volume fields.
func CollectCatalogItemRefs(spec *domain.DeviceSpec) []domain.CatalogItemRefSpec {
	if spec == nil {
		return nil
	}
	var refs []domain.CatalogItemRefSpec

	if spec.Os != nil && spec.Os.CatalogItemRef != nil {
		refs = append(refs, *spec.Os.CatalogItemRef)
	}

	if spec.Applications != nil {
		for _, app := range *spec.Applications {
			if ref, _ := ExtractAppCatalogItemRef(&app); ref != nil {
				refs = append(refs, *ref)
			}
			volRefs, _ := ExtractVolumeCatalogItemRefs(&app)
			refs = append(refs, volRefs...)
		}
	}
	return refs
}

// ExtractAppCatalogItemRef returns the catalog item ref and application name
// from an application provider spec, if it uses a catalog item ref provider type.
func ExtractAppCatalogItemRef(app *domain.ApplicationProviderSpec) (*domain.CatalogItemRefSpec, *string) {
	appType, err := app.GetAppType()
	if err != nil {
		return nil, nil
	}

	var source domain.CatalogItemRefSource
	var name *string
	var providerType domain.ApplicationProviderType
	switch appType {
	case domain.AppTypeContainer:
		a, err := app.AsContainerApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
		providerType = a.Type()
	case domain.AppTypeHelm:
		a, err := app.AsHelmApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
		providerType = a.Type()
	case domain.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
		providerType = a.Type()
	case domain.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return nil, nil
		}
		source = &a
		name = a.Name
		providerType = a.Type()
	default:
		return nil, nil
	}

	if providerType != domain.CatalogItemRefApplicationProviderType {
		return nil, nil
	}

	spec, err := source.AsCatalogItemRefApplicationProviderSpec()
	if err != nil {
		return nil, nil
	}
	return &spec.CatalogItemRef, name
}

// ExtractVolumeCatalogItemRefs returns all catalog item refs from an application's
// volumes, along with the application name.
func ExtractVolumeCatalogItemRefs(app *domain.ApplicationProviderSpec) ([]domain.CatalogItemRefSpec, *string) {
	appType, err := app.GetAppType()
	if err != nil {
		return nil, nil
	}

	var volumes *[]domain.ApplicationVolume
	var name *string
	switch appType {
	case domain.AppTypeContainer:
		a, err := app.AsContainerApplication()
		if err != nil {
			return nil, nil
		}
		volumes = a.Volumes
		name = a.Name
	case domain.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return nil, nil
		}
		volumes = a.Volumes
		name = a.Name
	case domain.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return nil, nil
		}
		volumes = a.Volumes
		name = a.Name
	default:
		return nil, nil
	}

	if volumes == nil {
		return nil, nil
	}

	var refs []domain.CatalogItemRefSpec
	for _, vol := range *volumes {
		volType, err := vol.Type()
		if err != nil {
			continue
		}
		switch volType {
		case domain.ImageApplicationVolumeProviderType:
			provider, err := vol.AsImageVolumeProviderSpec()
			if err != nil || provider.Image.CatalogItemRef == nil {
				continue
			}
			refs = append(refs, *provider.Image.CatalogItemRef)
		case domain.ImageMountApplicationVolumeProviderType:
			provider, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil || provider.Image.CatalogItemRef == nil {
				continue
			}
			refs = append(refs, *provider.Image.CatalogItemRef)
		}
	}
	return refs, name
}

// ValidateCatalogItemRefs checks that all catalog item references in a device spec
// point to existing catalog items with valid versions. When store is nil, validation
// is skipped.
func ValidateCatalogItemRefs(ctx context.Context, orgId uuid.UUID, store catalogstore.Store, spec *domain.DeviceSpec) domain.Status {
	if store == nil {
		return domain.StatusOK()
	}

	refs := CollectCatalogItemRefs(spec)
	if len(refs) == 0 {
		return domain.StatusOK()
	}

	type itemKey struct{ catalog, item string }
	fetched := make(map[itemKey]*domain.CatalogItem)

	for _, ref := range refs {
		key := itemKey{ref.Catalog, ref.Item}
		if _, seen := fetched[key]; seen {
			continue
		}

		catalogItem, err := store.GetItem(ctx, orgId, ref.Catalog, ref.Item)
		if err != nil {
			if errors.Is(err, flterrors.ErrResourceNotFound) {
				return domain.StatusBadRequest(fmt.Sprintf("catalog item %s/%s not found", ref.Catalog, ref.Item))
			}
			if errors.Is(err, flterrors.ErrParentResourceNotFound) {
				return domain.StatusBadRequest(fmt.Sprintf("catalog %s not found", ref.Catalog))
			}
			return domain.StatusInternalServerError(err.Error())
		}
		fetched[key] = catalogItem
	}

	for _, ref := range refs {
		key := itemKey{ref.Catalog, ref.Item}
		catalogItem := fetched[key]
		if catalogItem.Spec.FindVersion(ref.Version) == nil {
			return domain.StatusBadRequest(fmt.Sprintf("version %s not found in catalog item %s/%s", ref.Version, ref.Catalog, ref.Item))
		}
	}

	if spec.Applications != nil {
		for i, app := range *spec.Applications {
			ref, appName := ExtractAppCatalogItemRef(&app)
			if ref == nil {
				continue
			}

			key := itemKey{ref.Catalog, ref.Item}
			catalogItem := fetched[key]
			version := catalogItem.Spec.FindVersion(ref.Version)

			configSchema := getEffectiveConfigSchema(catalogItem, version)
			if configSchema == nil {
				continue
			}

			appBytes, err := json.Marshal(&app)
			if err != nil {
				return domain.StatusInternalServerError(fmt.Sprintf("failed to marshal application %d: %v", i, err))
			}

			dec := json.NewDecoder(strings.NewReader(string(appBytes)))
			dec.UseNumber()
			var appData interface{}
			if err := dec.Decode(&appData); err != nil {
				return domain.StatusInternalServerError(fmt.Sprintf("failed to decode application %d: %v", i, err))
			}

			appIdentifier := fmt.Sprintf("application[%d]", i)
			if appName != nil {
				appIdentifier = fmt.Sprintf("application %q", *appName)
			}

			if errs := validateDataAgainstConfigSchema(configSchema, appData); len(errs) > 0 {
				return domain.StatusBadRequest(
					fmt.Sprintf("%s does not conform to configSchema of catalog item %s/%s version %s: %v",
						appIdentifier, ref.Catalog, ref.Item, ref.Version, errors.Join(errs...)))
			}
		}
	}

	return domain.StatusOK()
}

func getEffectiveConfigSchema(item *domain.CatalogItem, version *domain.CatalogItemVersion) *map[string]interface{} {
	if version != nil && version.ConfigSchema != nil {
		return version.ConfigSchema
	}
	if item.Spec.Defaults != nil && item.Spec.Defaults.ConfigSchema != nil {
		return item.Spec.Defaults.ConfigSchema
	}
	return nil
}

func validateDataAgainstConfigSchema(schema *map[string]interface{}, data interface{}) []error {
	if schema == nil {
		return nil
	}

	schemaCopy := make(map[string]interface{}, len(*schema))
	for k, v := range *schema {
		schemaCopy[k] = v
	}
	schemaCopy["additionalProperties"] = true

	schemaBytes, err := json.Marshal(schemaCopy)
	if err != nil {
		return []error{fmt.Errorf("failed to marshal configSchema: %v", err)}
	}

	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(u string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema references are forbidden")
	}
	if err := compiler.AddResource("configSchema.json", strings.NewReader(string(schemaBytes))); err != nil {
		return []error{fmt.Errorf("invalid configSchema: %v", err)}
	}

	compiled, err := compiler.Compile("configSchema.json")
	if err != nil {
		return []error{fmt.Errorf("invalid configSchema: %v", err)}
	}

	if err := compiled.Validate(data); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			var errs []error
			for _, detail := range ve.BasicOutput().Errors {
				if detail.Error != "" {
					errs = append(errs, fmt.Errorf("%s: %s", detail.InstanceLocation, detail.Error))
				}
			}
			if len(errs) > 0 {
				return errs
			}
		}
		return []error{err}
	}

	return nil
}
