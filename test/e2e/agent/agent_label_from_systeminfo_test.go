package agent_test

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	labelFromSystemInfoBuiltInLabelArch     = "arch"
	labelFromSystemInfoBuiltInLabelOS       = "os"
	labelFromSystemInfoCustomLabelSite      = "site"
	labelFromSystemInfoDefaultAliasLabel    = "alias"
	labelFromSystemInfoMissingBuiltInLabel  = "missingBuiltin"
	labelFromSystemInfoMissingCustomLabel   = "missingCustom"
	labelFromSystemInfoManualArchValue      = "manually-set"
	labelFromSystemInfoExpectedOSValue      = "linux"
	labelFromSystemInfoExpectedSiteName     = "my site"
	labelFromSystemInfoCustomKeySiteName    = "siteName"
	labelFromSystemInfoCustomKeyEmptyValue  = "emptyValue"
	labelFromSystemInfoCustomFieldPrefix    = "customInfo."
	labelFromSystemInfoFieldArchitecture    = "architecture"
	labelFromSystemInfoFieldOperatingSystem = "operatingSystem"
	labelFromSystemInfoFieldProductName     = "productName"
	labelFromSystemInfoFieldHostname        = "hostname"
	labelFromSystemInfoMissingFieldName     = "doesNotExist"
)

var (
	labelFromSystemInfoBuiltInFieldsScenario = labelFromSystemInfoScenario{
		name: "built-in systemInfo fields become labels",
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoFieldArchitecture,
			labelFromSystemInfoBuiltInLabelOS:   labelFromSystemInfoFieldOperatingSystem,
		},
		wantLabels: map[string]string{
			labelFromSystemInfoBuiltInLabelOS: labelFromSystemInfoExpectedOSValue,
		},
		wantLabelsFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoFieldArchitecture,
		},
	}

	labelFromSystemInfoCustomFieldsScenario = labelFromSystemInfoScenario{
		name:                     "customInfo fields become labels",
		overrideSystemInfoCustom: true,
		systemInfoCustom:         []string{labelFromSystemInfoCustomKeySiteName},
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoCustomLabelSite: labelFromSystemInfoCustomFieldPrefix + labelFromSystemInfoCustomKeySiteName,
		},
		wantLabels: map[string]string{
			labelFromSystemInfoCustomLabelSite: labelFromSystemInfoExpectedSiteName,
		},
		wantCustomInfo: map[string]string{
			labelFromSystemInfoCustomKeySiteName: labelFromSystemInfoExpectedSiteName,
		},
	}

	labelFromSystemInfoPrecedenceScenario = labelFromSystemInfoScenario{
		name: "default-labels override mapped labels",
		defaultLabels: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoManualArchValue,
		},
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoFieldArchitecture,
		},
		wantLabels: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoManualArchValue,
		},
	}

	labelFromSystemInfoDefaultAliasScenario = labelFromSystemInfoScenario{
		name: "default alias follows useful hostname behavior",
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoFieldArchitecture,
		},
		wantLabelsFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoFieldArchitecture,
		},
		wantDefaultAliasFromHostname: true,
	}

	labelFromSystemInfoExplicitAliasScenario = labelFromSystemInfoScenario{
		name: "explicit alias mapping uses productName",
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoDefaultAliasLabel: labelFromSystemInfoFieldProductName,
		},
	}

	labelFromSystemInfoMissingFieldsScenario = labelFromSystemInfoScenario{
		name: "missing mapped fields are skipped",
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch:    labelFromSystemInfoFieldArchitecture,
			labelFromSystemInfoMissingBuiltInLabel: labelFromSystemInfoMissingFieldName,
			labelFromSystemInfoMissingCustomLabel:  labelFromSystemInfoCustomFieldPrefix + labelFromSystemInfoMissingFieldName,
		},
		wantLabelsFromSystemInfo: map[string]string{
			labelFromSystemInfoBuiltInLabelArch: labelFromSystemInfoFieldArchitecture,
		},
		wantAbsentLabels: []string{
			labelFromSystemInfoMissingBuiltInLabel,
			labelFromSystemInfoMissingCustomLabel,
		},
	}

	labelFromSystemInfoCustomCollectionDisabledScenario = labelFromSystemInfoScenario{
		name:                     "customInfo mapping without collection is skipped",
		overrideSystemInfoCustom: true,
		systemInfoCustom:         []string{},
		labelFromSystemInfo: map[string]string{
			labelFromSystemInfoCustomLabelSite: labelFromSystemInfoCustomFieldPrefix + labelFromSystemInfoCustomKeySiteName,
		},
		wantAbsentLabels: []string{labelFromSystemInfoCustomLabelSite},
		wantAbsentCustomInfo: []string{
			labelFromSystemInfoCustomKeySiteName,
			labelFromSystemInfoCustomKeyEmptyValue,
		},
	}
)

var _ = Describe("SystemInfo label mapping", func() {
	var harness *e2e.Harness

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
	})

	It("When built-in systemInfo fields are mapped it should create matching labels", Label("88946", "sanity", "agent"), func() {
		scenario := labelFromSystemInfoBuiltInFieldsScenario

		By("Configuring the agent and completing enrollment")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment, systemInfo, and mapped labels")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoSystemInfo(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})

	It("When customInfo fields are mapped it should create matching labels", Label("88947", "sanity", "agent"), func() {
		scenario := labelFromSystemInfoCustomFieldsScenario

		By("Configuring the agent and completing enrollment")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment, customInfo, and mapped labels")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoCustomInfo(result, scenario)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})

	It("When default-labels conflict with mapped labels it should keep default-labels precedence", Label("88948", "sanity", "agent"), func() {
		scenario := labelFromSystemInfoPrecedenceScenario

		By("Configuring conflicting default-labels and systemInfo label mappings")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment and default-label precedence")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})

	It("When alias is not explicitly mapped it should apply default alias hostname behavior", Label("88949", "agent"), func() {
		scenario := labelFromSystemInfoDefaultAliasScenario

		By("Configuring label mapping without an explicit alias")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment, hostname, and default alias behavior")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoSystemInfo(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})

	It("When alias is explicitly mapped it should use the mapped field instead of hostname fallback", Label("88950", "agent"), func() {
		scenario := labelFromSystemInfoExplicitAliasScenario

		By("Configuring alias to map from productName")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment and explicit alias mapping")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateExplicitAliasFromProductName(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})

	It("When mapped fields are missing it should skip labels for those fields", Label("88951", "agent"), func() {
		scenario := labelFromSystemInfoMissingFieldsScenario

		By("Configuring mappings for existing and missing fields")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment and skipped missing-field labels")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})

	It("When custom collection is disabled it should skip customInfo mappings", Label("88952", "agent"), func() {
		scenario := labelFromSystemInfoCustomCollectionDisabledScenario

		By("Configuring customInfo mapping without enabling the custom collector")
		result, err := runLabelFromSystemInfoEnrollmentScenario(harness, scenario)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying enrollment and skipped customInfo mapping")
		Expect(validateLabelFromSystemInfoEnrollment(result)).To(Succeed())
		Expect(validateLabelFromSystemInfoCustomInfo(result, scenario)).To(Succeed())
		Expect(validateLabelFromSystemInfoLabels(result, scenario)).To(Succeed())
	})
})

type labelFromSystemInfoScenario struct {
	name                         string
	defaultLabels                map[string]string
	labelFromSystemInfo          map[string]string
	overrideSystemInfoCustom     bool
	systemInfoCustom             []string
	wantLabels                   map[string]string
	wantLabelsFromSystemInfo     map[string]string
	wantAbsentLabels             []string
	wantDefaultAliasFromHostname bool
	wantCustomInfo               map[string]string
	wantAbsentCustomInfo         []string
}

type labelFromSystemInfoScenarioResult struct {
	enrollmentRequest    *v1beta1.EnrollmentRequest
	device               *v1beta1.Device
	enrollmentLabels     map[string]string
	deviceLabels         map[string]string
	enrollmentSystemInfo v1beta1.DeviceSystemInfo
	deviceSystemInfo     v1beta1.DeviceSystemInfo
	approvalStatusCode   int
}

// runLabelFromSystemInfoEnrollmentScenario applies an agent configuration, starts a fresh enrollment,
// approves it, and returns both the EnrollmentRequest and Device state for assertions.
func runLabelFromSystemInfoEnrollmentScenario(harness *e2e.Harness, scenario labelFromSystemInfoScenario) (*labelFromSystemInfoScenarioResult, error) {
	if harness == nil {
		return nil, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(scenario.name) == "" {
		return nil, fmt.Errorf("scenario name is empty")
	}

	if err := harness.DeleteCurrentEnrollmentRequestFromAgentLogs(); err != nil {
		return nil, fmt.Errorf("deleting pre-existing enrollment request before %q: %w", scenario.name, err)
	}

	if err := harness.ResetAgentEnrollmentState(); err != nil {
		return nil, err
	}

	if err := configureAgentForLabelFromSystemInfoScenario(harness, scenario); err != nil {
		return nil, err
	}

	if err := harness.StartFlightCtlAgent(); err != nil {
		return nil, err
	}

	enrollmentID, err := harness.WaitForEnrollmentIDFromAgentLogs(TIMEOUT, POLLING)
	if err != nil {
		return nil, err
	}
	GinkgoWriter.Printf("SystemInfo label mapping scenario %q using enrollment ID %s\n", scenario.name, enrollmentID)

	enrollmentRequest, err := harness.WaitForEnrollmentRequestResource(enrollmentID, TIMEOUT, POLLING)
	if err != nil {
		return nil, err
	}

	labels, err := harness.TestResourceLabels()
	if err != nil {
		return nil, err
	}
	approvalStatus, err := harness.ApproveEnrollmentRequestWithLabels(enrollmentID, labels)
	if err != nil {
		return nil, err
	}

	device, err := harness.WaitForDeviceWithSystemInfo(enrollmentID, TIMEOUT, POLLING)
	if err != nil {
		return nil, err
	}
	if enrollmentRequest.Spec.DeviceStatus == nil {
		return nil, fmt.Errorf("enrollment request %s has nil deviceStatus", enrollmentID)
	}
	if device.Status == nil {
		return nil, fmt.Errorf("device %s has nil status", enrollmentID)
	}

	result := &labelFromSystemInfoScenarioResult{
		enrollmentRequest:    enrollmentRequest,
		device:               device,
		enrollmentLabels:     dereferenceLabels(enrollmentRequest.Spec.Labels),
		deviceLabels:         dereferenceLabels(device.Metadata.Labels),
		enrollmentSystemInfo: enrollmentRequest.Spec.DeviceStatus.SystemInfo,
		deviceSystemInfo:     device.Status.SystemInfo,
		approvalStatusCode:   approvalStatus,
	}
	GinkgoWriter.Printf("SystemInfo label mapping scenario %q labels: enrollment=%v device=%v\n", scenario.name, result.enrollmentLabels, result.deviceLabels)
	return result, nil
}

// configureAgentForLabelFromSystemInfoScenario writes the scenario-specific agent config
// before enrollment starts.
func configureAgentForLabelFromSystemInfoScenario(harness *e2e.Harness, scenario labelFromSystemInfoScenario) error {
	cfg, err := harness.GetAgentConfig()
	if err != nil {
		return fmt.Errorf("reading agent config for scenario %q: %w", scenario.name, err)
	}

	cfg.DefaultLabels = map[string]string{}
	cfg.LabelFromSystemInfo = map[string]string{}
	maps.Copy(cfg.DefaultLabels, scenario.defaultLabels)
	maps.Copy(cfg.LabelFromSystemInfo, scenario.labelFromSystemInfo)
	if scenario.overrideSystemInfoCustom {
		cfg.SystemInfoCustom = slices.Clone(scenario.systemInfoCustom)
	}

	if err := harness.SetAgentConfig(cfg); err != nil {
		return fmt.Errorf("setting agent config for scenario %q: %w", scenario.name, err)
	}
	return nil
}

// validateLabelFromSystemInfoEnrollment verifies the common enrollment result.
func validateLabelFromSystemInfoEnrollment(result *labelFromSystemInfoScenarioResult) error {
	if result == nil {
		return fmt.Errorf("scenario result is nil")
	}
	if result.approvalStatusCode != http.StatusOK {
		return fmt.Errorf("enrollment approval status = %d, want %d", result.approvalStatusCode, http.StatusOK)
	}
	if result.enrollmentRequest == nil {
		return fmt.Errorf("enrollment request is nil")
	}
	if result.device == nil {
		return fmt.Errorf("device is nil")
	}
	if result.enrollmentLabels == nil {
		return fmt.Errorf("enrollment labels are nil")
	}
	if result.deviceLabels == nil {
		return fmt.Errorf("device labels are nil")
	}
	return nil
}

// validateLabelFromSystemInfoSystemInfo verifies common systemInfo fields.
func validateLabelFromSystemInfoSystemInfo(result *labelFromSystemInfoScenarioResult) error {
	if result == nil {
		return fmt.Errorf("scenario result is nil")
	}
	enrollmentHostname := systemInfoValue(result.enrollmentSystemInfo, labelFromSystemInfoFieldHostname)
	if strings.TrimSpace(enrollmentHostname) == "" {
		return fmt.Errorf("enrollment request hostname is empty")
	}
	deviceHostname := systemInfoValue(result.deviceSystemInfo, labelFromSystemInfoFieldHostname)
	if strings.TrimSpace(deviceHostname) == "" {
		return fmt.Errorf("device hostname is empty")
	}
	if deviceHostname != enrollmentHostname {
		return fmt.Errorf("device hostname = %q, want enrollment hostname %q", deviceHostname, enrollmentHostname)
	}
	return nil
}

// validateLabelFromSystemInfoLabels verifies expected and absent labels on both
// the EnrollmentRequest and resulting Device.
func validateLabelFromSystemInfoLabels(result *labelFromSystemInfoScenarioResult, scenario labelFromSystemInfoScenario) error {
	if result == nil {
		return fmt.Errorf("scenario result is nil")
	}
	for key, value := range scenario.wantLabels {
		got, ok := result.enrollmentLabels[key]
		if !ok {
			return fmt.Errorf("enrollment request label %q is absent, want %q", key, value)
		}
		if got != value {
			return fmt.Errorf("enrollment request label %q = %q, want %q", key, got, value)
		}
		got, ok = result.deviceLabels[key]
		if !ok {
			return fmt.Errorf("device label %q is absent, want %q", key, value)
		}
		if got != value {
			return fmt.Errorf("device label %q = %q, want %q", key, got, value)
		}
	}

	for key, field := range scenario.wantLabelsFromSystemInfo {
		value := systemInfoValue(result.enrollmentSystemInfo, field)
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("enrollment request systemInfo field %q is empty for expected label %q", field, key)
		}
		if deviceValue := systemInfoValue(result.deviceSystemInfo, field); strings.TrimSpace(deviceValue) != "" && deviceValue != value {
			return fmt.Errorf("device systemInfo field %q = %q, want enrollment request value %q", field, deviceValue, value)
		}
		got, ok := result.enrollmentLabels[key]
		if !ok {
			return fmt.Errorf("enrollment request label %q is absent, want systemInfo.%s value %q", key, field, value)
		}
		if got != value {
			return fmt.Errorf("enrollment request label %q = %q, want systemInfo.%s value %q", key, got, field, value)
		}
		got, ok = result.deviceLabels[key]
		if !ok {
			return fmt.Errorf("device label %q is absent, want systemInfo.%s value %q", key, field, value)
		}
		if got != value {
			return fmt.Errorf("device label %q = %q, want systemInfo.%s value %q", key, got, field, value)
		}
	}

	for _, key := range scenario.wantAbsentLabels {
		if got, ok := result.enrollmentLabels[key]; ok {
			return fmt.Errorf("enrollment request label %q should be absent, got %q", key, got)
		}
		if got, ok := result.deviceLabels[key]; ok {
			return fmt.Errorf("device label %q should be absent, got %q", key, got)
		}
	}

	if scenario.wantDefaultAliasFromHostname {
		if err := validateDefaultAliasFromHostname(result); err != nil {
			return err
		}
	}
	return nil
}

// validateDefaultAliasFromHostname verifies default alias behavior for useful
// and non-useful hostnames.
func validateDefaultAliasFromHostname(result *labelFromSystemInfoScenarioResult) error {
	if result == nil {
		return fmt.Errorf("scenario result is nil")
	}
	enrollmentHostname := systemInfoValue(result.enrollmentSystemInfo, labelFromSystemInfoFieldHostname)
	deviceHostname := systemInfoValue(result.deviceSystemInfo, labelFromSystemInfoFieldHostname)
	if isUsefulLabelFromSystemInfoHostname(enrollmentHostname) {
		got, ok := result.enrollmentLabels[labelFromSystemInfoDefaultAliasLabel]
		if !ok {
			return fmt.Errorf("enrollment request alias is absent, want hostname %q", enrollmentHostname)
		}
		if got != enrollmentHostname {
			return fmt.Errorf("enrollment request alias = %q, want hostname %q", got, enrollmentHostname)
		}
	} else if got, ok := result.enrollmentLabels[labelFromSystemInfoDefaultAliasLabel]; ok {
		return fmt.Errorf("enrollment request alias should be absent for hostname %q, got %q", enrollmentHostname, got)
	}

	if isUsefulLabelFromSystemInfoHostname(deviceHostname) {
		got, ok := result.deviceLabels[labelFromSystemInfoDefaultAliasLabel]
		if !ok {
			return fmt.Errorf("device alias is absent, want hostname %q", deviceHostname)
		}
		if got != deviceHostname {
			return fmt.Errorf("device alias = %q, want hostname %q", got, deviceHostname)
		}
	} else if got, ok := result.deviceLabels[labelFromSystemInfoDefaultAliasLabel]; ok {
		return fmt.Errorf("device alias should be absent for hostname %q, got %q", deviceHostname, got)
	}
	return nil
}

// isUsefulLabelFromSystemInfoHostname returns false for hostnames that should
// not become the default alias.
func isUsefulLabelFromSystemInfoHostname(hostname string) bool {
	if hostname == "" || hostname == "(none)" {
		return false
	}

	h := strings.ToLower(hostname)
	if strings.HasPrefix(h, "localhost") {
		return false
	}
	return h != "127.0.0.1" && h != "::1"
}

// validateExplicitAliasFromProductName verifies an explicit alias mapping uses
// the live productName reported during enrollment and by the device.
func validateExplicitAliasFromProductName(result *labelFromSystemInfoScenarioResult) error {
	if result == nil {
		return fmt.Errorf("scenario result is nil")
	}

	enrollmentProductName := systemInfoValue(result.enrollmentSystemInfo, labelFromSystemInfoFieldProductName)
	if strings.TrimSpace(enrollmentProductName) == "" {
		return fmt.Errorf("enrollment request productName is empty")
	}
	deviceProductName := systemInfoValue(result.deviceSystemInfo, labelFromSystemInfoFieldProductName)
	if strings.TrimSpace(deviceProductName) == "" {
		return fmt.Errorf("device productName is empty")
	}
	if deviceProductName != enrollmentProductName {
		return fmt.Errorf("device productName = %q, want enrollment productName %q", deviceProductName, enrollmentProductName)
	}
	got, ok := result.enrollmentLabels[labelFromSystemInfoDefaultAliasLabel]
	if !ok {
		return fmt.Errorf("enrollment request alias is absent, want productName %q", enrollmentProductName)
	}
	if got != enrollmentProductName {
		return fmt.Errorf("enrollment request alias = %q, want productName %q", got, enrollmentProductName)
	}
	got, ok = result.deviceLabels[labelFromSystemInfoDefaultAliasLabel]
	if !ok {
		return fmt.Errorf("device alias is absent, want productName %q", deviceProductName)
	}
	if got != deviceProductName {
		return fmt.Errorf("device alias = %q, want productName %q", got, deviceProductName)
	}
	return nil
}

// validateLabelFromSystemInfoCustomInfo verifies expected and absent customInfo
// fields on both the EnrollmentRequest and resulting Device.
func validateLabelFromSystemInfoCustomInfo(result *labelFromSystemInfoScenarioResult, scenario labelFromSystemInfoScenario) error {
	if result == nil {
		return fmt.Errorf("scenario result is nil")
	}

	enrollmentCustomInfo := customInfoMap(result.enrollmentSystemInfo)
	deviceCustomInfo := customInfoMap(result.deviceSystemInfo)
	for key, value := range scenario.wantCustomInfo {
		got, ok := enrollmentCustomInfo[key]
		if !ok {
			return fmt.Errorf("enrollment request customInfo %q is absent, want %q", key, value)
		}
		if got != value {
			return fmt.Errorf("enrollment request customInfo %q = %q, want %q", key, got, value)
		}
		got, ok = deviceCustomInfo[key]
		if !ok {
			return fmt.Errorf("device customInfo %q is absent, want %q", key, value)
		}
		if got != value {
			return fmt.Errorf("device customInfo %q = %q, want %q", key, got, value)
		}
	}

	for _, key := range scenario.wantAbsentCustomInfo {
		if got, ok := enrollmentCustomInfo[key]; ok {
			return fmt.Errorf("enrollment request customInfo %q should be absent, got %q", key, got)
		}
		if got, ok := deviceCustomInfo[key]; ok {
			return fmt.Errorf("device customInfo %q should be absent, got %q", key, got)
		}
	}
	return nil
}

// dereferenceLabels returns a copy of labels, or an empty map when labels is nil.
func dereferenceLabels(labels *map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(*labels))
	maps.Copy(out, *labels)
	return out
}

// customInfoMap returns a copy of customInfo, or an empty map when customInfo is nil.
func customInfoMap(systemInfo v1beta1.DeviceSystemInfo) map[string]string {
	if systemInfo.CustomInfo == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(*systemInfo.CustomInfo))
	maps.Copy(out, *systemInfo.CustomInfo)
	return out
}

// systemInfoValue returns the value for a systemInfo key, or an empty string when the key is absent.
func systemInfoValue(systemInfo v1beta1.DeviceSystemInfo, key string) string {
	switch key {
	case labelFromSystemInfoFieldArchitecture:
		return systemInfo.Architecture
	case labelFromSystemInfoFieldOperatingSystem:
		return systemInfo.OperatingSystem
	}

	value, found := systemInfo.Get(key)
	if !found {
		return ""
	}
	return value
}
