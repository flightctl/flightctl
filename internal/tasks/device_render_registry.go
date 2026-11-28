package tasks

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appspec"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/samber/lo"
)

func (t *DeviceRenderLogic) WithRegistryConfigSnapshotFactory(factory registryconfig.SnapshotFactory) *DeviceRenderLogic {
	t.registrySnapshotFactory = factory
	return t
}

func collectRegistryPullNeeds(apps []domain.ApplicationProviderSpec, osImage, osDeltaImage string) ([]registryconfig.PullNeed, error) {
	var needs []registryconfig.PullNeed
	add := func(ref string, consumer registryconfig.Consumer, user string) {
		if ref == "" {
			return
		}
		destination := "containers:" + user
		if consumer == registryconfig.ConsumerHelmChart {
			destination = "helm-chart:root"
		}
		if consumer == registryconfig.ConsumerOS {
			destination = "os:root"
		}
		needs = append(needs, registryconfig.PullNeed{Reference: ref, Consumer: consumer, User: user, Destination: destination})
	}
	hints := func(entries *[]api.ImageDeltaHint, consumer registryconfig.Consumer, user string) {
		for _, hint := range lo.FromPtr(entries) {
			add(hint.TargetImage, consumer, user)
			add(hint.DeltaImage, consumer, user)
		}
	}
	image := func(provider imageHintProvider, consumer registryconfig.Consumer, user string) {
		spec, err := provider.AsImageApplicationProviderSpec()
		if err != nil {
			return
		}
		add(spec.Image, consumer, user)
		add(lo.FromPtr(spec.DeltaImage), consumer, user)
		nestedConsumer := consumer
		if consumer == registryconfig.ConsumerHelmChart {
			nestedConsumer = registryconfig.ConsumerHelmImage
		}
		hints(spec.DeltaImages, nestedConsumer, user)
	}
	volumes := func(entries *[]api.ApplicationVolume, consumer registryconfig.Consumer, user string) {
		for _, volume := range lo.FromPtr(entries) {
			kind, err := volume.Type()
			if err != nil {
				continue
			}
			switch kind {
			case api.ImageApplicationVolumeProviderType:
				spec, err := volume.AsImageVolumeProviderSpec()
				if err == nil {
					add(spec.Image.Reference, consumer, user)
				}
			case api.ImageMountApplicationVolumeProviderType:
				spec, err := volume.AsImageMountVolumeProviderSpec()
				if err == nil {
					add(spec.Image.Reference, consumer, user)
				}
			}
		}
	}
	for _, app := range apps {
		kind, err := app.GetAppType()
		if err != nil {
			return nil, errors.New("decoding registry application type")
		}
		switch kind {
		case api.AppTypeContainer:
			spec, err := app.AsContainerApplication()
			if err != nil {
				return nil, errors.New("decoding container registry needs")
			}
			user := spec.RunAsWithDefault().String()
			image(&spec, registryconfig.ConsumerContainer, user)
			volumes(spec.Volumes, registryconfig.ConsumerContainer, user)
		case api.AppTypeCompose:
			spec, err := app.AsComposeApplication()
			if err != nil {
				return nil, errors.New("decoding Compose registry needs")
			}
			user := spec.RunAsWithDefault().String()
			image(&spec, registryconfig.ConsumerContainer, user)
			volumes(spec.Volumes, registryconfig.ConsumerContainer, user)
			if inline, err := spec.AsInlineApplicationProviderSpec(); err == nil {
				hints(inline.DeltaImages, registryconfig.ConsumerContainer, user)
				if parsed, err := appspec.ParseComposeFromSpec(inline.Inline); err == nil {
					for _, service := range parsed.Services {
						add(service.Image, registryconfig.ConsumerContainer, user)
					}
				}
			}
		case api.AppTypeQuadlet:
			spec, err := app.AsQuadletApplication()
			if err != nil {
				return nil, errors.New("decoding Quadlet registry needs")
			}
			user := spec.RunAsWithDefault().String()
			image(&spec, registryconfig.ConsumerContainer, user)
			volumes(spec.Volumes, registryconfig.ConsumerContainer, user)
			if inline, err := spec.AsInlineApplicationProviderSpec(); err == nil {
				hints(inline.DeltaImages, registryconfig.ConsumerContainer, user)
				if refs, err := appspec.ParseQuadletImageReferencesFromSpec(inline.Inline); err == nil {
					for _, ref := range refs {
						add(ref, registryconfig.ConsumerContainer, user)
					}
				}
			}
		case api.AppTypeHelm:
			spec, err := app.AsHelmApplication()
			if err != nil {
				return nil, errors.New("decoding Helm registry needs")
			}
			image(&spec, registryconfig.ConsumerHelmChart, api.RootUsername.String())
		default:
			return nil, fmt.Errorf("unsupported rendered registry consumer %q", kind)
		}
	}
	add(osImage, registryconfig.ConsumerOS, api.RootUsername.String())
	add(osDeltaImage, registryconfig.ConsumerOS, api.RootUsername.String())
	slices.SortFunc(needs, func(a, b registryconfig.PullNeed) int {
		return cmp.Or(cmp.Compare(a.Reference, b.Reference), cmp.Compare(a.Consumer, b.Consumer), cmp.Compare(a.User, b.User), cmp.Compare(a.Destination, b.Destination))
	})
	return slices.Compact(needs), nil
}

type registryAuthoredFiles map[string]bool

func (files registryAuthoredFiles) auth(need registryconfig.PullNeed) bool {
	switch need.Consumer {
	case registryconfig.ConsumerOS:
		return files[consts.OSTreeAuthPath]
	case registryconfig.ConsumerHelmChart:
		return files[consts.HelmRegistryConfigPath] || files[consts.RootContainersAuthPath]
	default:
		return need.User == api.RootUsername.String() && files[consts.RootContainersAuthPath]
	}
}

func (files registryAuthoredFiles) policy(consumer registryconfig.Consumer) bool {
	// Helm chart options and the device-local CRI backend can require settings outside containers policy.
	if consumer == registryconfig.ConsumerHelmChart || consumer == registryconfig.ConsumerHelmImage {
		return false
	}
	for path := range files {
		if path == consts.ContainersRegistriesConfPath || (filepath.Dir(path) == consts.ContainersRegistriesConfDir && strings.HasSuffix(path, ".conf")) {
			return true
		}
	}
	return false
}

func (t *deviceRenderState) renderRegistryConfigResources(ctx context.Context, rendered *RenderedSpec, osHints *deviceservice.RenderedOSHints) error {
	var apps []domain.ApplicationProviderSpec
	if len(rendered.Applications) > 0 {
		if err := json.Unmarshal(rendered.Applications, &apps); err != nil {
			return errors.New("decoding registry applications")
		}
	}
	var providers []domain.ConfigProviderSpec
	if len(rendered.Config) > 0 {
		if err := json.Unmarshal(rendered.Config, &providers); err != nil {
			return errors.New("decoding registry authored files")
		}
	}
	authored := registryAuthoredFiles{}
	for _, provider := range providers {
		inline, err := provider.AsInlineConfigProviderSpec()
		if err != nil {
			return errors.New("decoding rendered inline registry files")
		}
		for _, file := range inline.Inline {
			authored[filepath.Clean(file.Path)] = true
		}
	}
	deltaImage := ""
	if osHints != nil {
		deltaImage = lo.FromPtr(osHints.DeltaImage)
	}
	needs, err := collectRegistryPullNeeds(apps, rendered.OsImage, deltaImage)
	if err != nil {
		return err
	}
	if len(needs) == 0 {
		return nil
	}
	for i := range needs {
		needs[i].SuppressAuthentication = authored.auth(needs[i])
	}
	var candidates []registryconfig.Candidate
	params := domain.ListRepositoriesParams{}
	for {
		page, status := t.repositorySvc.ListRepositories(ctx, t.orgId, params)
		if err := common.ApiStatusToErr(status); err != nil {
			return fmt.Errorf("listing registry repositories: %w", err)
		}
		for _, repo := range page.Items {
			candidates = append(candidates, registryconfig.Candidate{OrganizationID: t.orgId, Repository: repo})
		}
		if lo.FromPtr(page.Metadata.Continue) == "" {
			break
		}
		params.Continue = page.Metadata.Continue
	}
	snapshot, err := t.registrySnapshotFactory(t.orgId, candidates)
	if err != nil {
		return err
	}
	composition, err := snapshot.Compose(needs)
	if err != nil {
		return err
	}
	resources, err := registryConfigResources(composition, authored)
	if err != nil {
		return err
	}
	if len(resources) == 0 {
		return nil
	}
	rendered.ConfigResources, err = json.Marshal(resources)
	return err
}

func registryConfigResources(composition *registryconfig.Composition, files registryAuthoredFiles) ([]domain.ConfigResource, error) {
	endpoints := map[string]registryconfig.EndpointConfig{}
	for _, endpoint := range composition.Endpoints {
		endpoints[endpoint.Registry] = endpoint
	}
	grouped := map[string]map[string]*api.RegistryConfigAccess{}
	for _, selection := range composition.Selections {
		if selection.Outcome != registryconfig.OutcomeSelected && selection.Outcome != registryconfig.OutcomeAllowed {
			continue
		}
		endpoint, ok := endpoints[selection.Registry]
		if !ok {
			return nil, errors.New("registry selection has no endpoint configuration")
		}
		var auth *api.OciAuth
		if selection.Supplier != nil && !selection.Need.SuppressAuthentication && !files.auth(selection.Need) {
			spec, err := selection.Supplier.Spec.AsOciRepoSpec()
			if err != nil {
				return nil, errors.New("decoding selected registry authentication")
			}
			auth = spec.OciAuth
		}
		if auth == nil && endpoint.CABundle == "" && files.policy(selection.Need.Consumer) {
			continue
		}
		authBytes, err := json.Marshal(auth)
		if err != nil {
			return nil, errors.New("encoding selected registry authentication")
		}
		key := selection.Scope + "\x00" + string(authBytes)
		if grouped[selection.Registry] == nil {
			grouped[selection.Registry] = map[string]*api.RegistryConfigAccess{}
		}
		access := grouped[selection.Registry][key]
		if access == nil {
			access = &api.RegistryConfigAccess{Scope: lo.ToPtr(selection.Scope), Authentication: auth}
			grouped[selection.Registry][key] = access
		}
		consumer := api.RegistryConfigConsumer{Type: api.RegistryConfigConsumerType(selection.Need.Consumer)}
		if selection.Need.User != "" {
			consumer.User = lo.ToPtr(api.Username(selection.Need.User))
		}
		access.Consumers = append(access.Consumers, consumer)
	}
	var resources []domain.ConfigResource
	registries := make([]string, 0, len(grouped))
	for registry := range grouped {
		registries = append(registries, registry)
	}
	slices.Sort(registries)
	for _, registry := range registries {
		endpoint := endpoints[registry]
		resource := api.RegistryConfigResource{Type: api.Registry, Name: "registry:" + registry, Registry: registry, Scheme: api.RegistryConfigResourceScheme(endpoint.Scheme), SkipServerVerification: lo.ToPtr(endpoint.SkipServerVerification)}
		if endpoint.CABundle != "" {
			resource.CaCrt = lo.ToPtr(base64.StdEncoding.EncodeToString([]byte(endpoint.CABundle)))
		}
		for _, access := range grouped[registry] {
			slices.SortFunc(access.Consumers, func(a, b api.RegistryConfigConsumer) int {
				return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(lo.FromPtr(a.User), lo.FromPtr(b.User)))
			})
			access.Consumers = slices.CompactFunc(access.Consumers, func(a, b api.RegistryConfigConsumer) bool {
				return a.Type == b.Type && lo.FromPtr(a.User) == lo.FromPtr(b.User)
			})
			resource.Access = append(resource.Access, *access)
		}
		slices.SortFunc(resource.Access, func(a, b api.RegistryConfigAccess) int {
			ac, _ := json.Marshal(a.Consumers)
			bc, _ := json.Marshal(b.Consumers)
			return cmp.Or(cmp.Compare(lo.FromPtr(a.Scope), lo.FromPtr(b.Scope)), strings.Compare(string(ac), string(bc)))
		})
		var union domain.ConfigResource
		if err := union.FromRegistryConfigResource(resource); err != nil {
			return nil, err
		}
		resources = append(resources, union)
	}
	if errs := api.ValidateConfigResources(resources); len(errs) > 0 {
		return nil, fmt.Errorf("invalid rendered registry configuration: %w", errors.Join(errs...))
	}
	return resources, nil
}
