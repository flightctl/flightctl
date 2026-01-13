package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

// Compile-time check that ImageBuild implements model.ResourceInterface
var _ model.ResourceInterface = (*ImageBuild)(nil)

// Compile-time check that ImageExport implements model.ResourceInterface
var _ model.ResourceInterface = (*ImageExport)(nil)

type ImageBuild struct {
	model.Resource

	// The desired state, stored as opaque JSON object.
	Spec *model.JSONField[api.ImageBuildSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *model.JSONField[api.ImageBuildStatus] `gorm:"type:jsonb"`
}

func (i ImageBuild) String() string {
	val, _ := json.Marshal(i)
	return string(val)
}

func NewImageBuildFromApiResource(resource *api.ImageBuild) (*ImageBuild, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ImageBuild{}, nil
	}

	status := api.ImageBuildStatus{}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &ImageBuild{
		Resource: model.Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   model.MakeJSONField(resource.Spec),
		Status: model.MakeJSONField(status),
	}, nil
}

func ImageBuildAPIVersion() string {
	return api.ImageBuildAPIVersion
}

type ImageBuildAPIResourceOption func(*imageBuildAPIResourceOptions)

type imageBuildAPIResourceOptions struct {
	imageExports []api.ImageExport
}

func WithImageExports(imageExports []api.ImageExport) ImageBuildAPIResourceOption {
	return func(o *imageBuildAPIResourceOptions) {
		o.imageExports = imageExports
	}
}

func (i *ImageBuild) ToApiResource(opts ...ImageBuildAPIResourceOption) (*api.ImageBuild, error) {
	if i == nil {
		return &api.ImageBuild{}, nil
	}

	options := imageBuildAPIResourceOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	spec := api.ImageBuildSpec{}
	if i.Spec != nil {
		spec = i.Spec.Data
	}

	status := api.ImageBuildStatus{}
	if i.Status != nil {
		status = i.Status.Data
	}

	result := &api.ImageBuild{
		ApiVersion: ImageBuildAPIVersion(),
		Kind:       string(api.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name:              lo.ToPtr(i.Name),
			CreationTimestamp: lo.ToPtr(i.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(i.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(i.Resource.Annotations)),
			Generation:        i.Generation,
			Owner:             i.Owner,
			ResourceVersion:   lo.Ternary(i.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(i.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}

	// Add imageexports field if provided
	if len(options.imageExports) > 0 {
		result.Imageexports = &options.imageExports
	}

	return result, nil
}

func ImageBuildsToApiResource(imageBuilds []ImageBuild, cont *string, numRemaining *int64) (api.ImageBuildList, error) {
	return ImageBuildsToApiResourceWithOptions(imageBuilds, cont, numRemaining, nil)
}

func ImageBuildsToApiResourceWithOptions(imageBuilds []ImageBuild, cont *string, numRemaining *int64, imageExportsMap map[string][]api.ImageExport) (api.ImageBuildList, error) {
	imageBuildList := make([]api.ImageBuild, len(imageBuilds))
	for i, imageBuild := range imageBuilds {
		var apiOpts []ImageBuildAPIResourceOption
		if imageExportsMap != nil {
			if exports, ok := imageExportsMap[imageBuild.Name]; ok && len(exports) > 0 {
				apiOpts = append(apiOpts, WithImageExports(exports))
			}
		}
		apiResource, _ := imageBuild.ToApiResource(apiOpts...)
		imageBuildList[i] = *apiResource
	}
	ret := api.ImageBuildList{
		ApiVersion: ImageBuildAPIVersion(),
		Kind:       api.ImageBuildListKind,
		Items:      imageBuildList,
		Metadata:   v1beta1.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (i *ImageBuild) GetKind() string {
	return string(api.ResourceKindImageBuild)
}

func (i *ImageBuild) HasNilSpec() bool {
	return i.Spec == nil
}

func (i *ImageBuild) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*ImageBuild)
	if !ok {
		return false
	}
	if other == nil {
		return false
	}
	if i.Spec == nil && other.Spec == nil {
		return true
	}
	if (i.Spec == nil && other.Spec != nil) || (i.Spec != nil && other.Spec == nil) {
		return false
	}
	// Compare specs by JSON marshaling
	thisSpec, _ := json.Marshal(i.Spec.Data)
	otherSpec, _ := json.Marshal(other.Spec.Data)
	return string(thisSpec) == string(otherSpec)
}

func (i *ImageBuild) GetStatusAsJson() ([]byte, error) {
	if i.Status == nil {
		return []byte("{}"), nil
	}
	return i.Status.MarshalJSON()
}

// ImageExport model
type ImageExport struct {
	model.Resource

	// The desired state, stored as opaque JSON object.
	Spec *model.JSONField[api.ImageExportSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *model.JSONField[api.ImageExportStatus] `gorm:"type:jsonb"`
}

func (i ImageExport) String() string {
	val, _ := json.Marshal(i)
	return string(val)
}

func NewImageExportFromApiResource(resource *api.ImageExport) (*ImageExport, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ImageExport{}, nil
	}

	status := api.ImageExportStatus{}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &ImageExport{
		Resource: model.Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   model.MakeJSONField(resource.Spec),
		Status: model.MakeJSONField(status),
	}, nil
}

func ImageExportAPIVersion() string {
	return api.ImageExportAPIVersion
}

func (i *ImageExport) ToApiResource() (*api.ImageExport, error) {
	if i == nil {
		return &api.ImageExport{}, nil
	}

	spec := api.ImageExportSpec{}
	if i.Spec != nil {
		spec = i.Spec.Data
	}

	status := api.ImageExportStatus{}
	if i.Status != nil {
		status = i.Status.Data
	}

	return &api.ImageExport{
		ApiVersion: ImageExportAPIVersion(),
		Kind:       string(api.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name:              lo.ToPtr(i.Name),
			CreationTimestamp: lo.ToPtr(i.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(i.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(i.Resource.Annotations)),
			Generation:        i.Generation,
			Owner:             i.Owner,
			ResourceVersion:   lo.Ternary(i.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(i.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func ImageExportsToApiResource(imageExports []ImageExport, cont *string, numRemaining *int64) (api.ImageExportList, error) {
	imageExportList := make([]api.ImageExport, len(imageExports))
	for i, imageExport := range imageExports {
		apiResource, _ := imageExport.ToApiResource()
		imageExportList[i] = *apiResource
	}
	ret := api.ImageExportList{
		ApiVersion: ImageExportAPIVersion(),
		Kind:       api.ImageExportListKind,
		Items:      imageExportList,
		Metadata:   v1beta1.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (i *ImageExport) GetKind() string {
	return string(api.ResourceKindImageExport)
}

func (i *ImageExport) HasNilSpec() bool {
	return i.Spec == nil
}

func (i *ImageExport) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*ImageExport)
	if !ok {
		return false
	}
	if other == nil {
		return false
	}
	if i.Spec == nil && other.Spec == nil {
		return true
	}
	if (i.Spec == nil && other.Spec != nil) || (i.Spec != nil && other.Spec == nil) {
		return false
	}
	// Compare specs by JSON marshaling
	thisSpec, _ := json.Marshal(i.Spec.Data)
	otherSpec, _ := json.Marshal(other.Spec.Data)
	return string(thisSpec) == string(otherSpec)
}

func (i *ImageExport) GetStatusAsJson() ([]byte, error) {
	if i.Status == nil {
		return []byte("{}"), nil
	}
	return i.Status.MarshalJSON()
}

// Field selector support for ImageExport
var imageExportSpecSelectors = map[selector.SelectorName]selector.SelectorType{
	selector.NewSelectorName("spec.source.imageBuildRef"): selector.String,
}

// ResolveSelector resolves a field selector name to a SelectorField for ImageExport
func (i *ImageExport) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := imageExportSpecSelectors[name]; exists {
		return makeImageExportJSONBSelectorField(name, typ)
	}
	return nil, fmt.Errorf("unable to resolve selector for image export")
}

// ListSelectors returns all available field selectors for ImageExport
func (i *ImageExport) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(imageExportSpecSelectors))
	for sn := range imageExportSpecSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

// makeImageExportJSONBSelectorField creates a SelectorField for JSONB fields in ImageExport
func makeImageExportJSONBSelectorField(selectorName selector.SelectorName, selectorType selector.SelectorType) (*selector.SelectorField, error) {
	selectorStr := selectorName.String()
	if len(selectorStr) == 0 {
		return nil, fmt.Errorf("jsonb selector name cannot be empty")
	}

	var params strings.Builder
	parts := strings.Split(selectorStr, ".")
	params.WriteString(parts[0])

	lastIndex := len(parts[1:]) - 1
	for i, part := range parts[1:] {
		if i == lastIndex && selectorType != selector.Jsonb {
			params.WriteString(" ->> '")
		} else {
			params.WriteString(" -> '")
		}
		params.WriteString(part)
		params.WriteString("'")
	}

	return &selector.SelectorField{
		Name:      selectorName,
		Type:      selectorType,
		FieldName: params.String(),
		FieldType: "jsonb",
	}, nil
}
