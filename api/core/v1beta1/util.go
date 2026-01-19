package v1beta1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

type DeviceCompletionCount struct {
	Count               int64
	SameRenderedVersion bool
	SameTemplateVersion bool
	UpdatingReason      UpdateState
	UpdateTimedOut      bool
}

type HookActionType string

const (
	HookActionTypeRun HookActionType = "run"
)

type HookConditionType string

const (
	HookConditionTypePathOp     HookConditionType = "path"
	HookConditionTypeExpression HookConditionType = "expression"
)

type ConfigProviderType string

const (
	GitConfigProviderType        ConfigProviderType = "gitRef"
	HttpConfigProviderType       ConfigProviderType = "httpRef"
	InlineConfigProviderType     ConfigProviderType = "inline"
	KubernetesSecretProviderType ConfigProviderType = "secretRef"
)

type ApplicationProviderType string

const (
	ImageApplicationProviderType  ApplicationProviderType = "image"
	InlineApplicationProviderType ApplicationProviderType = "inline"
)

type ApplicationVolumeProviderType string

const (
	ImageApplicationVolumeProviderType      ApplicationVolumeProviderType = "image"
	MountApplicationVolumeProviderType      ApplicationVolumeProviderType = "mount"
	ImageMountApplicationVolumeProviderType ApplicationVolumeProviderType = "image_mount"
)

// Type returns the type of the action.
func (t HookAction) Type() (HookActionType, error) {
	var data map[HookActionType]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		return "", err
	}

	types := []HookActionType{
		HookActionTypeRun,
	}
	for _, t := range types {
		if _, exists := data[t]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine hook action type: %+v", data)
}

// Type returns the type of the condition.
func (t HookCondition) Type() (HookConditionType, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		var data HookConditionExpression
		if err := json.Unmarshal(t.union, &data); err != nil {
			return "", err
		}
		return HookConditionTypeExpression, nil
	}

	types := []HookConditionType{
		HookConditionTypePathOp,
	}
	for _, t := range types {
		if _, exists := data[string(t)]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine hook condition type: %+v", data)
}

// Type returns the type of the config provider.
func (c ConfigProviderSpec) Type() (ConfigProviderType, error) {
	var data map[ConfigProviderType]interface{}
	if err := json.Unmarshal(c.union, &data); err != nil {
		return "", err
	}

	types := []ConfigProviderType{
		GitConfigProviderType,
		HttpConfigProviderType,
		InlineConfigProviderType,
		KubernetesSecretProviderType,
	}
	for _, t := range types {
		if _, exists := data[t]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine config provider type: %+v", data)
}

// Type returns the provider type (image or inline) for compose applications.
func (c ComposeApplication) Type() (ApplicationProviderType, error) {
	return getApplicationProviderType(c.union)
}

// Type returns the provider type (image or inline) for quadlet applications.
func (q QuadletApplication) Type() (ApplicationProviderType, error) {
	return getApplicationProviderType(q.union)
}

func getApplicationProviderType(union json.RawMessage) (ApplicationProviderType, error) {
	var data map[ApplicationProviderType]interface{}
	if err := json.Unmarshal(union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ImageApplicationProviderType]; exists {
		return ImageApplicationProviderType, nil
	}

	if _, exists := data[InlineApplicationProviderType]; exists {
		return InlineApplicationProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application provider type: %+v", data)
}

// GetAppType returns the application type from the discriminator.
func (a ApplicationProviderSpec) GetAppType() (AppType, error) {
	discriminator, err := a.Discriminator()
	if err != nil {
		return "", err
	}
	return AppType(discriminator), nil
}

// GetName returns the application name from the underlying type.
func (a ApplicationProviderSpec) GetName() (*string, error) {
	appType, err := a.GetAppType()
	if err != nil {
		return nil, err
	}
	switch appType {
	case AppTypeContainer:
		app, err := a.AsContainerApplication()
		if err != nil {
			return nil, err
		}
		return app.Name, nil
	case AppTypeHelm:
		app, err := a.AsHelmApplication()
		if err != nil {
			return nil, err
		}
		return app.Name, nil
	case AppTypeCompose:
		app, err := a.AsComposeApplication()
		if err != nil {
			return nil, err
		}
		return app.Name, nil
	case AppTypeQuadlet:
		app, err := a.AsQuadletApplication()
		if err != nil {
			return nil, err
		}
		return app.Name, nil
	default:
		return nil, fmt.Errorf("unknown app type: %s", appType)
	}
}

func (c ApplicationVolume) Type() (ApplicationVolumeProviderType, error) {
	var data map[ApplicationVolumeProviderType]interface{}
	if err := json.Unmarshal(c.union, &data); err != nil {
		return "", err
	}

	_, image := data[ImageApplicationVolumeProviderType]
	_, mount := data[MountApplicationVolumeProviderType]

	if image && mount {
		return ImageMountApplicationVolumeProviderType, nil
	}

	if image {
		return ImageApplicationVolumeProviderType, nil
	}

	if mount {
		return MountApplicationVolumeProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application volume type: %+v", data)
}

func (c ApplicationVolume) GetReclaimPolicy() ApplicationVolumeReclaimPolicy {
	if c.ReclaimPolicy == nil || *c.ReclaimPolicy == "" {
		return Retain
	}
	return *c.ReclaimPolicy
}

// Some functions that we provide to users.  In case of a missing label,
// we may get an interface{} rather than string because
// ExecuteGoTemplateOnDevice() converts the Device struct to a map.
// Therefore our functions here need to ensure we get a string, and if
// not then they return an empty string.  Note that this will only
// happen if the "missingkey=zero" option is used in the template.  If
// "missingkey=error" is used, the template execution will fail and we
// won't get to this point.
func GetGoTemplateFuncMap() template.FuncMap {
	stringOrDefault := func(s any) string {
		str, ok := s.(string)
		if ok {
			return str
		}
		strPtr, ok := s.(*string)
		if ok && strPtr != nil {
			return *strPtr
		}
		return ""
	}

	toUpper := func(s any) string {
		return strings.ToUpper(stringOrDefault(s))
	}

	toLower := func(s any) string {
		return strings.ToLower(stringOrDefault(s))
	}

	replace := func(old, new string, input any) string {
		return strings.Replace(stringOrDefault(input), old, new, -1)
	}

	getOrDefault := func(m *map[string]string, key string, defaultValue string) string {
		if m == nil {
			return defaultValue
		}
		if val, ok := (*m)[key]; ok {
			return val
		}
		return defaultValue
	}

	return template.FuncMap{
		"upper":        toUpper,
		"lower":        toLower,
		"replace":      replace,
		"getOrDefault": getOrDefault,
	}
}

// This function wraps template.Execute.  Instead of passing the device directly,
// it converts it into a map first.  This has two purposes:
// 1. The user-provided template uses the yaml/json API format (e.g., lower case)
// 2. The map contains only the device fields we allow access to
func ExecuteGoTemplateOnDevice(t *template.Template, dev *Device) (string, error) {
	devMap := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   dev.Metadata.Name,
			"labels": dev.Metadata.Labels,
		},
	}

	buf := new(bytes.Buffer)
	err := t.Execute(buf, devMap)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// String converts a MatchExpression into its string representation.
// Example formats:
// - Exists: "key"
// - DoesNotExist: "!key"
// - In: "key in (val1, val2)"
// - NotIn: "key notin (val1, val2)"
func (e MatchExpression) String() string {
	var sb strings.Builder

	switch e.Operator {
	case Exists:
		sb.WriteString(e.Key) // Exists: Just the key
	case DoesNotExist:
		sb.WriteString("!") // Prepend the "not exists" operator
		sb.WriteString(e.Key)
	case In:
		if e.Values != nil {
			sb.WriteString(e.Key)
			sb.WriteString(" in ")
			sb.WriteString("(" + strings.Join(*e.Values, ", ") + ")")
		}
	case NotIn:
		if e.Values != nil {
			sb.WriteString(e.Key)
			sb.WriteString(" notin ")
			sb.WriteString("(" + strings.Join(*e.Values, ", ") + ")")
		}
	default:
		// Return empty string for unsupported operators
		return ""
	}
	return sb.String()
}

// GetConsoles returns the list of DeviceConsole objects, or an empty list if the field is nil.
func (rd DeviceSpec) GetConsoles() []DeviceConsole {
	if rd.Consoles == nil {
		return []DeviceConsole{}
	} else {
		return *rd.Consoles
	}
}

type SensitiveDataHider interface {
	HideSensitiveData() error
}

// SensitiveDataPreserver is implemented by types that can preserve sensitive data from an existing object
// when the new object contains the masked placeholder value ("*****").
type SensitiveDataPreserver interface {
	PreserveSensitiveData(existing SensitiveDataPreserver) error
}

const MaskedValuePlaceholder = "*****"

func hideValue(value *string) {
	if value != nil {
		*value = MaskedValuePlaceholder
	}
}

// preserveValue preserves the old value if the new value is the masked placeholder
func preserveValue(newValue, oldValue *string) {
	if newValue != nil && *newValue == MaskedValuePlaceholder && oldValue != nil {
		*newValue = *oldValue
	}
}

func (r *Repository) HideSensitiveData() error {
	if r == nil {
		return nil
	}
	spec := r.Spec
	specType, err := spec.Discriminator()
	if err != nil {
		return err
	}
	switch specType {
	case string(RepoSpecTypeOci):
		oci, err := spec.AsOciRepoSpec()
		if err != nil {
			return err
		}
		if oci.OciAuth != nil {
			dockerAuth, err := oci.OciAuth.AsDockerAuth()
			if err == nil {
				hideValue(&dockerAuth.Password)
				if err := oci.OciAuth.FromDockerAuth(dockerAuth); err != nil {
					return err
				}
			}
		}
		if err := spec.FromOciRepoSpec(oci); err != nil {
			return err
		}
		r.Spec = spec
		return nil
	case string(RepoSpecTypeHttp):
		http, err := spec.AsHttpRepoSpec()
		if err != nil {
			return err
		}
		hideValue(http.HttpConfig.Password)
		hideValue(http.HttpConfig.Token)
		hideValue(http.HttpConfig.TlsKey)
		hideValue(http.HttpConfig.TlsCrt)
		if err := spec.FromHttpRepoSpec(http); err != nil {
			return err
		}
		r.Spec = spec
		return nil
	case string(RepoSpecTypeGit):
		ssh, err := spec.GetSshRepoSpec()
		if err != nil {
			// Not an SSH repo spec (plain GenericRepoSpec), nothing to hide
			return nil
		}
		hideValue(ssh.SshConfig.SshPrivateKey)
		hideValue(ssh.SshConfig.PrivateKeyPassphrase)
		if err := spec.FromSshRepoSpec(ssh); err != nil {
			return err
		}
		r.Spec = spec
		return nil
	default:
		return fmt.Errorf("unknown repository type: %s", specType)
	}
}

func (r *RepositoryList) HideSensitiveData() error {
	if r == nil {
		return nil
	}
	for i := range r.Items {
		if err := r.Items[i].HideSensitiveData(); err != nil {
			return err
		}
	}
	return nil
}

// PreserveSensitiveData preserves sensitive data from the existing repository when the new repository
// contains the masked placeholder value ("*****"). This allows users to update a repository without
// needing to re-provide credentials if they haven't changed.
func (r *Repository) PreserveSensitiveData(existing SensitiveDataPreserver) error {
	if r == nil || existing == nil {
		return nil
	}

	existingRepo, ok := existing.(*Repository)
	if !ok {
		return fmt.Errorf("existing object is not a Repository")
	}

	spec := r.Spec
	specType, err := spec.Discriminator()
	if err != nil {
		return err
	}

	existingSpecType, err := existingRepo.Spec.Discriminator()
	if err != nil {
		return err
	}

	// If the repository types don't match, nothing to preserve
	if specType != existingSpecType {
		return nil
	}

	switch specType {
	case string(RepoSpecTypeOci):
		oci, err := spec.AsOciRepoSpec()
		if err != nil {
			return err
		}
		existingOci, err := existingRepo.Spec.AsOciRepoSpec()
		if err != nil {
			return err
		}
		if oci.OciAuth != nil && existingOci.OciAuth != nil {
			dockerAuth, err := oci.OciAuth.AsDockerAuth()
			if err == nil {
				existingDockerAuth, existingErr := existingOci.OciAuth.AsDockerAuth()
				if existingErr == nil {
					preserveValue(&dockerAuth.Password, &existingDockerAuth.Password)
					if err := oci.OciAuth.FromDockerAuth(dockerAuth); err != nil {
						return err
					}
				}
			}
		}
		if err := spec.FromOciRepoSpec(oci); err != nil {
			return err
		}
		r.Spec = spec
		return nil

	case string(RepoSpecTypeHttp):
		http, err := spec.AsHttpRepoSpec()
		if err != nil {
			return err
		}
		existingHttp, err := existingRepo.Spec.AsHttpRepoSpec()
		if err != nil {
			return err
		}
		preserveValue(http.HttpConfig.Password, existingHttp.HttpConfig.Password)
		preserveValue(http.HttpConfig.Token, existingHttp.HttpConfig.Token)
		preserveValue(http.HttpConfig.TlsKey, existingHttp.HttpConfig.TlsKey)
		preserveValue(http.HttpConfig.TlsCrt, existingHttp.HttpConfig.TlsCrt)
		if err := spec.FromHttpRepoSpec(http); err != nil {
			return err
		}
		r.Spec = spec
		return nil

	case string(RepoSpecTypeGit):
		ssh, err := spec.GetSshRepoSpec()
		if err != nil {
			// Not an SSH repo spec (plain GenericRepoSpec), nothing to preserve
			return nil
		}
		existingSsh, err := existingRepo.Spec.GetSshRepoSpec()
		if err != nil {
			// Existing is not an SSH repo spec, nothing to preserve
			return nil
		}
		preserveValue(ssh.SshConfig.SshPrivateKey, existingSsh.SshConfig.SshPrivateKey)
		preserveValue(ssh.SshConfig.PrivateKeyPassphrase, existingSsh.SshConfig.PrivateKeyPassphrase)
		if err := spec.FromSshRepoSpec(ssh); err != nil {
			return err
		}
		r.Spec = spec
		return nil

	default:
		return nil // Unknown type, nothing to preserve
	}
}

func (a *AuthProvider) HideSensitiveData() error {
	if a == nil {
		return nil
	}

	// Check the discriminator to determine the provider type
	discriminator, err := a.Spec.Discriminator()
	if err != nil {
		return err
	}

	switch discriminator {
	case string(Oidc):
		oidcSpec, err := a.Spec.AsOIDCProviderSpec()
		if err != nil {
			return err
		}
		hideValue(oidcSpec.ClientSecret)
		if err := a.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
			return err
		}

	case string(Oauth2):
		oauth2Spec, err := a.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return err
		}
		hideValue(oauth2Spec.ClientSecret)
		if err := a.Spec.FromOAuth2ProviderSpec(oauth2Spec); err != nil {
			return err
		}
	case string(Openshift):
		openshiftSpec, err := a.Spec.AsOpenShiftProviderSpec()
		if err != nil {
			return err
		}
		hideValue(openshiftSpec.ClientSecret)
		if err := a.Spec.FromOpenShiftProviderSpec(openshiftSpec); err != nil {
			return err
		}
	case string(Aap):
		aapSpec, err := a.Spec.AsAapProviderSpec()
		if err != nil {
			return err
		}
		hideValue(aapSpec.ClientSecret)
		if err := a.Spec.FromAapProviderSpec(aapSpec); err != nil {
			return err
		}
	}
	return nil
}

func (a *AuthProviderList) HideSensitiveData() error {
	if a == nil {
		return nil
	}
	for i := range a.Items {
		if err := a.Items[i].HideSensitiveData(); err != nil {
			return err
		}
	}
	return nil
}

func (a *AuthConfig) HideSensitiveData() error {
	if a == nil {
		return nil
	}
	if a.Providers != nil {
		for i := range *a.Providers {
			if err := (*a.Providers)[i].HideSensitiveData(); err != nil {
				return err
			}
		}
	}
	return nil
}

// PreserveSensitiveData preserves sensitive data from the existing auth provider when the new provider
// contains the masked placeholder value ("*****"). This allows users to update an auth provider without
// needing to re-provide credentials if they haven't changed.
func (a *AuthProvider) PreserveSensitiveData(existing SensitiveDataPreserver) error {
	if a == nil || existing == nil {
		return nil
	}

	existingAP, ok := existing.(*AuthProvider)
	if !ok {
		return fmt.Errorf("existing object is not an AuthProvider")
	}

	discriminator, err := a.Spec.Discriminator()
	if err != nil {
		return err
	}

	existingDiscriminator, err := existingAP.Spec.Discriminator()
	if err != nil {
		return err
	}

	// If the provider types don't match, nothing to preserve
	if discriminator != existingDiscriminator {
		return nil
	}

	switch discriminator {
	case string(Oidc):
		oidcSpec, err := a.Spec.AsOIDCProviderSpec()
		if err != nil {
			return err
		}
		existingOidcSpec, err := existingAP.Spec.AsOIDCProviderSpec()
		if err != nil {
			return err
		}
		preserveValue(oidcSpec.ClientSecret, existingOidcSpec.ClientSecret)
		if err := a.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
			return err
		}

	case string(Oauth2):
		oauth2Spec, err := a.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return err
		}
		existingOauth2Spec, err := existingAP.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return err
		}
		preserveValue(oauth2Spec.ClientSecret, existingOauth2Spec.ClientSecret)
		if err := a.Spec.FromOAuth2ProviderSpec(oauth2Spec); err != nil {
			return err
		}

	case string(Openshift):
		openshiftSpec, err := a.Spec.AsOpenShiftProviderSpec()
		if err != nil {
			return err
		}
		existingOpenshiftSpec, err := existingAP.Spec.AsOpenShiftProviderSpec()
		if err != nil {
			return err
		}
		preserveValue(openshiftSpec.ClientSecret, existingOpenshiftSpec.ClientSecret)
		if err := a.Spec.FromOpenShiftProviderSpec(openshiftSpec); err != nil {
			return err
		}

	case string(Aap):
		aapSpec, err := a.Spec.AsAapProviderSpec()
		if err != nil {
			return err
		}
		existingAapSpec, err := existingAP.Spec.AsAapProviderSpec()
		if err != nil {
			return err
		}
		preserveValue(aapSpec.ClientSecret, existingAapSpec.ClientSecret)
		if err := a.Spec.FromAapProviderSpec(aapSpec); err != nil {
			return err
		}
	}
	return nil
}
