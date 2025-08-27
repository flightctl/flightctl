package v1alpha1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"text/template"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/samber/lo"
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
	ImageApplicationVolumeProviderType ApplicationVolumeProviderType = "image"
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

// Type returns the type of the application provider.
func (a ApplicationProviderSpec) Type() (ApplicationProviderType, error) {
	return getApplicationType(a.union)
}

func getApplicationType(union json.RawMessage) (ApplicationProviderType, error) {
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

func (c ApplicationVolume) Type() (ApplicationVolumeProviderType, error) {
	var data map[ApplicationVolumeProviderType]interface{}
	if err := json.Unmarshal(c.union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ImageApplicationVolumeProviderType]; exists {
		return ImageApplicationVolumeProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application volume type: %+v", data)
}

func PercentageAsInt(p Percentage) (int, error) {
	index := strings.Index(p, "%")
	if index <= 0 || index != len(p)-1 {
		return 0, fmt.Errorf("%s is not in percentage format", p)
	}
	percentage, err := strconv.ParseInt(p[:index], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse percentage value: %w", err)
	}
	if percentage < 0 || percentage > 100 {
		return 0, fmt.Errorf("percentage must be between 0 and 100, got %d", percentage)
	}
	return int(percentage), nil
}

func DeviceSpecsAreEqual(d1, d2 DeviceSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(d1), reflect.ValueOf(d2))
}

func FleetSpecsAreEqual(f1, f2 FleetSpec) bool {
	return util.DeepEqualWithUnionHandling(reflect.ValueOf(f1), reflect.ValueOf(f2))
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

// MatchExpressionsToString converts a list of MatchExpressions into a formatted string.
// Each MatchExpression is represented by its string form, separated by ", ".
func MatchExpressionsToString(exprs ...MatchExpression) string {
	if len(exprs) == 0 {
		return ""
	}

	var sb strings.Builder
	for i, e := range exprs {
		sb.WriteString(e.String())
		if i < len(exprs)-1 {
			sb.WriteString(", ")
		}
	}
	return sb.String()
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

func GetNextDeviceRenderedVersion(annotations map[string]string, deviceStatus *DeviceStatus) (string, error) {
	// Get service-side renderedVersion version from annotations
	var renderedVersion int64 = 0
	renderedVersionString, ok := annotations[DeviceAnnotationRenderedVersion]
	if ok {
		var err error
		renderedVersion, err = strconv.ParseInt(renderedVersionString, 10, 64)
		if err != nil {
			return "", err
		}
	}

	// Get device-reported version from status (if available)
	var deviceRenderedVersion int64 = 0
	if deviceStatus != nil && deviceStatus.Config.RenderedVersion != "" {
		var err error
		deviceRenderedVersion, err = strconv.ParseInt(deviceStatus.Config.RenderedVersion, 10, 64)
		if err != nil {
			return "", err
		}
	}

	// max(rendered, device_reported) + 1
	nextVersion := max(renderedVersion, deviceRenderedVersion) + 1
	return strconv.FormatInt(nextVersion, 10), nil
}

type SensitiveDataHider interface {
	HideSensitiveData() error
}

func hideValue(value *string) {
	if value != nil {
		*value = "*****"
	}
}

func (r *Repository) HideSensitiveData() error {
	if r == nil {
		return nil
	}
	spec := r.Spec

	_, err := spec.GetGenericRepoSpec()
	if err != nil {
		gitHttpSpec, err := spec.GetHttpRepoSpec()
		if err == nil {
			hideValue(gitHttpSpec.HttpConfig.Password)
			hideValue(gitHttpSpec.HttpConfig.TlsKey)
			hideValue(gitHttpSpec.HttpConfig.TlsCrt)
			if err := spec.FromHttpRepoSpec(gitHttpSpec); err != nil {
				return err
			}

		} else {
			gitSshRepoSpec, err := spec.GetSshRepoSpec()
			if err == nil {
				hideValue(gitSshRepoSpec.SshConfig.SshPrivateKey)
				hideValue(gitSshRepoSpec.SshConfig.PrivateKeyPassphrase)
				if err := spec.FromSshRepoSpec(gitSshRepoSpec); err != nil {
					return err
				}
			}
		}
	}
	r.Spec = spec
	return nil
}

func (r *RepositoryList) HideSensitiveData() error {
	if r == nil {
		return nil
	}
	for _, repo := range r.Items {
		if err := repo.HideSensitiveData(); err != nil {
			return err
		}
	}
	return nil
}

// GetBaseEvent creates a base event with common fields
func GetBaseEvent(ctx context.Context, resourceKind ResourceKind, resourceName string, reason EventReason, message string, details *EventDetails) *Event {
	var actorStr string
	if actor := ctx.Value(consts.EventActorCtxKey); actor != nil {
		actorStr = actor.(string)
	}

	var componentStr string
	if component := ctx.Value(consts.EventSourceComponentCtxKey); component != nil {
		componentStr = component.(string)
	}

	// Generate a UUID for the event name to ensure k8s compliance
	eventName := uuid.New().String()

	event := Event{
		Metadata: ObjectMeta{
			Name: lo.ToPtr(eventName),
		},
		InvolvedObject: ObjectReference{
			Kind: string(resourceKind),
			Name: resourceName,
		},
		Source: EventSource{
			Component: componentStr,
		},
		Actor: actorStr,
	}

	// Add request ID to the event for correlation
	if reqID := ctx.Value(middleware.RequestIDKey); reqID != nil {
		event.Metadata.Annotations = &map[string]string{EventAnnotationRequestID: reqID.(string)}
	}

	event.Reason = reason
	event.Message = message
	event.Type = GetEventType(reason)
	event.Details = details

	return &event
}

// warningReasons contains all event reasons that should result in Warning events
var warningReasons = map[EventReason]struct{}{
	EventReasonResourceCreationFailed:          {},
	EventReasonResourceUpdateFailed:            {},
	EventReasonResourceDeletionFailed:          {},
	EventReasonDeviceDecommissionFailed:        {},
	EventReasonEnrollmentRequestApprovalFailed: {},
	EventReasonDeviceApplicationDegraded:       {},
	EventReasonDeviceApplicationError:          {},
	EventReasonDeviceCPUCritical:               {},
	EventReasonDeviceCPUWarning:                {},
	EventReasonDeviceMemoryCritical:            {},
	EventReasonDeviceMemoryWarning:             {},
	EventReasonDeviceDiskCritical:              {},
	EventReasonDeviceDiskWarning:               {},
	EventReasonDeviceDisconnected:              {},
	EventReasonDeviceSpecInvalid:               {},
	EventReasonFleetInvalid:                    {},
	EventReasonDeviceMultipleOwnersDetected:    {},
	EventReasonDeviceUpdateFailed:              {},
	EventReasonInternalTaskFailed:              {},
	EventReasonResourceSyncInaccessible:        {},
	EventReasonResourceSyncParsingFailed:       {},
	EventReasonResourceSyncSyncFailed:          {},
	EventReasonFleetRolloutFailed:              {},
}

// GetEventType determines the event type based on the event reason
func GetEventType(reason EventReason) EventType {
	if _, contains := warningReasons[reason]; contains {
		return Warning
	}
	return Normal
}
