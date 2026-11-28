package appinspection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	inspection "github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/helm"
	"github.com/flightctl/flightctl/internal/oci"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

// EffectiveApplication supplies execution inputs in memory. Durable work holds
// only identities and reference facts; callers reload the application on delivery.
type EffectiveApplication struct {
	Spec              domain.ApplicationProviderSpec
	Device            *domain.Device
	RenderedConfig    []byte
	InvalidHelmConfig bool
}

type InputResolver struct {
	inspector *Inspector
	helm      inspection.HelmRenderer
	cache     oci.DigestCache
	cfg       *config.Config
	log       logrus.FieldLogger
}

func NewInputResolver(inspector *Inspector, renderer inspection.HelmRenderer, cache oci.DigestCache, cfg *config.Config, log logrus.FieldLogger) *InputResolver {
	return &InputResolver{inspector: inspector, helm: renderer, cache: cache, cfg: cfg, log: log}
}

func DeclaredReferences(app *domain.ApplicationProviderSpec) []inspectionstore.ReferenceInput {
	// Admission retains valid references for targeting; execution reports malformed references.
	refs, _ := referenceInputs(inspection.DeclaredReferences(app))
	return refs
}

func referenceInputs(refs []inspection.Reference) ([]inspectionstore.ReferenceInput, bool) {
	var inputs []inspectionstore.ReferenceInput
	valid := true
	for _, ref := range refs {
		image, hasOCITransport := strings.CutPrefix(ref.Image, "oci://")
		if !hasOCITransport {
			image = strings.TrimPrefix(image, "docker://")
		}
		input := inspectionstore.ReferenceInput{
			Reference: image, Role: model.InspectionArtifactRole(ref.Role), Mechanism: model.InspectionPullMechanism(ref.ConsumerKind),
		}
		normalized, err := inspectionstore.NormalizeReference(input)
		if err != nil {
			valid = false
			continue
		}
		input.Reference = normalized.Reference
		inputs = append(inputs, input)
	}
	return inputs, valid
}

func fingerprint(value any) (model.InspectionFingerprint, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return model.InspectionFingerprint{}, err
	}
	return sha256.Sum256(encoded), nil
}

type preparedInput struct {
	identity model.ApplicationInspectionInput
	run      func(context.Context) inspection.Result
}

func (r *InputResolver) prepare(ctx context.Context, orgID uuid.UUID, app EffectiveApplication, pinned digest.Digest) (preparedInput, model.InspectionDiagnostic) {
	appType, err := app.Spec.GetAppType()
	if err != nil {
		return preparedInput{}, model.InspectionParseFailed
	}
	declared := inspection.DeclaredReferences(&app.Spec)
	refs, valid := referenceInputs(declared)
	if !valid {
		return preparedInput{}, model.InspectionParseFailed
	}
	identity := model.ApplicationInspectionInput{Provider: model.InspectionProvider(appType)}
	identity.Declared, err = fingerprint(refs)
	if err != nil {
		return preparedInput{}, model.InspectionParseFailed
	}
	switch appType {
	case domain.AppTypeContainer:
		identity.Format, identity.InlineFingerprint = model.InspectionFormatInline, identity.Declared
		return preparedInput{identity, func(context.Context) inspection.Result { return inspection.Result{} }}, ""
	case domain.AppTypeHelm:
		return r.prepareHelm(orgID, app, identity, refs)
	case domain.AppTypeCompose, domain.AppTypeQuadlet:
		if len(declared) == 0 || !slices.ContainsFunc(declared, func(ref inspection.Reference) bool { return ref.Role == inspection.RolePackage }) {
			identity.Format = model.InspectionFormatInline
			identity.InlineFingerprint, err = fingerprint(app.Spec)
			if err != nil {
				return preparedInput{}, model.InspectionParseFailed
			}
			return preparedInput{identity, func(context.Context) inspection.Result {
				if result := inspection.InspectSpec(&app.Spec).Inline; result != nil {
					return *result
				}
				return inspection.Result{Diagnostics: []inspection.Diagnostic{inspection.DiagnosticParseFailed}}
			}}, ""
		}
		return r.preparePackage(ctx, orgID, app, appType, identity, declared, pinned)
	default:
		return preparedInput{}, model.InspectionParseFailed
	}
}

func (r *InputResolver) prepareHelm(orgID uuid.UUID, app EffectiveApplication, identity model.ApplicationInspectionInput, refs []inspectionstore.ReferenceInput) (preparedInput, model.InspectionDiagnostic) {
	spec, err := app.Spec.AsHelmApplication()
	if err != nil || app.InvalidHelmConfig || len(refs) != 1 || refs[0].Mechanism != model.InspectionPullHelmChart {
		return preparedInput{}, model.InspectionParseFailed
	}
	release, err := helm.TargetRelease(spec, app.Device)
	if err != nil {
		return preparedInput{}, model.InspectionParseFailed
	}
	aliases, err := helm.TargetRepositoryAliases(app.RenderedConfig)
	if err != nil {
		return preparedInput{}, model.InspectionParseFailed
	}
	values, err := fingerprint(struct {
		Values any
		Files  any
	}{spec.Values, spec.ValuesFiles})
	if err != nil {
		return preparedInput{}, model.InspectionParseFailed
	}
	deps, err := fingerprint(aliases)
	if err != nil {
		return preparedInput{}, model.InspectionParseFailed
	}
	identity.Format = model.InspectionFormatChart
	identity.Helm = &model.InspectionHelmIdentity{
		Values: values, Dependencies: deps, ChartReference: refs[0].Reference,
		ReleaseName: release.Name, Namespace: release.Namespace, FreshFor: r.cfg.EffectiveHelmImageRefsCacheTTL(),
	}
	if release.Upgrade {
		identity.Helm.RenderContext[0] = 1
	}
	return preparedInput{identity, func(ctx context.Context) inspection.Result {
		image, err := spec.AsImageApplicationProviderSpec()
		if err != nil {
			return inspection.Result{Diagnostics: []inspection.Diagnostic{inspection.DiagnosticParseFailed}}
		}
		access, err := helm.ResolveOCIRepositorySpec(ctx, r.inspector.repositories, orgID, image.Image, nil)
		if err != nil {
			return inspection.Result{Diagnostics: []inspection.Diagnostic{inspection.DiagnosticContentUnavailable}}
		}
		return inspection.InspectHelm(ctx, r.helm, spec, app.Device, app.RenderedConfig, access)
	}}, ""
}

func (r *InputResolver) packageDigest(ctx context.Context, orgID uuid.UUID, declared []inspection.Reference) (digest.Digest, func(context.Context) (*remote.Repository, error), error) {
	endpoint, err := packageEndpoint(declared)
	if err != nil {
		return "", nil, err
	}
	var repo *remote.Repository
	repository := func(ctx context.Context) (*remote.Repository, error) {
		if repo == nil {
			var err error
			repo, err = r.inspector.packageRepository(ctx, orgID, endpoint)
			if err != nil {
				return nil, err
			}
		}
		return repo, nil
	}
	raw, err := oci.CachedImageDigest(ctx, r.log, r.cache, orgID, endpoint.String(), r.cfg.EffectiveImageDigestCacheTTL(), func(ctx context.Context) (string, error) {
		repo, err := repository(ctx)
		if err != nil {
			return "", err
		}
		desc, err := repo.Resolve(ctx, endpoint.Reference)
		return desc.Digest.String(), err
	})
	if err != nil {
		return "", nil, err
	}
	revision := digest.Digest(raw)
	if revision.Validate() != nil {
		return "", nil, errors.New("invalid inspection package digest")
	}
	return revision, repository, nil
}

func (r *InputResolver) preparePackage(ctx context.Context, orgID uuid.UUID, app EffectiveApplication, appType domain.AppType, identity model.ApplicationInspectionInput, declared []inspection.Reference, pinned digest.Digest) (preparedInput, model.InspectionDiagnostic) {
	revision := pinned
	var repo *remote.Repository
	var err error
	if pinned == "" {
		var repository func(context.Context) (*remote.Repository, error)
		revision, repository, err = r.packageDigest(ctx, orgID, declared)
		if err == nil {
			repo, err = repository(ctx)
		}
	} else {
		var endpoint registry.Reference
		endpoint, err = packageEndpoint(declared)
		if err == nil {
			repo, err = r.inspector.packageRepository(ctx, orgID, endpoint)
		}
	}
	if err != nil {
		return preparedInput{}, model.InspectionContentUnavailable
	}
	pkg := inspection.PreparePackage(ctx, repo, revision.String(), oci.DeviceImagePlatform(app.Device))
	if len(pkg.Diagnostics) != 0 {
		return preparedInput{}, model.InspectionDiagnostic(pkg.Diagnostics[0])
	}
	if pkg.ParentDigest != revision {
		return preparedInput{}, model.InspectionContentUnavailable
	}
	identity.ParentDigest = revision
	identity.Format = model.InspectionFormat(pkg.Format)
	if platform := pkg.Platform; platform != nil {
		identity.Platform = platform.OS + "/" + platform.Architecture
		if platform.Variant != "" {
			identity.Platform += "/" + platform.Variant
		}
	}
	return preparedInput{identity, func(ctx context.Context) inspection.Result { return pkg.Inspect(ctx, appType).Result }}, ""
}

func (p preparedInput) inspect(ctx context.Context, app *domain.ApplicationProviderSpec) inspectionstore.Attempt {
	result := p.run(ctx)
	refs, valid := referenceInputs(append(inspection.DeclaredReferences(app), result.References...))
	attempt := inspectionstore.Attempt{Input: p.identity, State: model.InspectionComplete, References: refs}
	for _, code := range result.Diagnostics {
		attempt.DiagnosticCodes = append(attempt.DiagnosticCodes, model.InspectionDiagnostic(code))
	}
	if !valid && !slices.Contains(attempt.DiagnosticCodes, model.InspectionParseFailed) {
		attempt.DiagnosticCodes = append(attempt.DiagnosticCodes, model.InspectionParseFailed)
	}
	if len(attempt.DiagnosticCodes) != 0 {
		attempt.State = model.InspectionFinishedIncomplete
	}
	return attempt
}
