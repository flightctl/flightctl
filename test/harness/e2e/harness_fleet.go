package e2e

import (
	"fmt"
	"strconv"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/sirupsen/logrus"
)

func (h *Harness) CreateFleetDeviceSpec(deviceImageTag string, additionalConfigs ...v1beta1.ConfigProviderSpec) (v1beta1.DeviceSpec, error) {

	var deviceSpec v1beta1.DeviceSpec

	// Set Os.Image only if deviceImageTag is provided
	if deviceImageTag != "" {
		deviceSpec.Os = &v1beta1.DeviceOsSpec{
			Image: util.NewDeviceImageReference(deviceImageTag).String(),
		}
	}

	// Set Config only if config specs are provided
	if len(additionalConfigs) > 0 {
		deviceSpec.Config = &additionalConfigs
	}

	return deviceSpec, nil
}

func (h *Harness) WaitForFleetContents(fleetName string, description string, condition func(fleet *v1beta1.Fleet) bool, timeout string) {
	waitForResourceContents(fleetName, description, func(id string) (*v1beta1.Fleet, error) {
		response, err := h.Client.GetFleetWithResponse(h.Context, id, nil)
		Expect(err).NotTo(HaveOccurred())
		if response.JSON200 == nil {
			logrus.Errorf("An error happened retrieving fleet: %+v", response)
			return nil, fmt.Errorf("error retrieving fleet: %s", id)
		}
		return response.JSON200, nil
	}, condition, timeout)
}

func (h *Harness) GetFleet(fleetName string) (*v1beta1.Fleet, error) {
	response, err := h.Client.GetFleetWithResponse(h.Context, fleetName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get fleet: %s %w", fleetName, err)
	}
	if response == nil || response.JSON200 == nil {
		return nil, fmt.Errorf("fleet: %s response is nil", fleetName)
	}
	return response.JSON200, nil
}

// Create a test fleet resource
func (h *Harness) CreateOrUpdateTestFleet(testFleetName string, fleetSpecOrSelector interface{}, deviceSpec ...v1beta1.DeviceSpec) error {
	testFleet := v1beta1.Fleet{
		ApiVersion: v1beta1.FleetAPIVersion,
		Kind:       v1beta1.FleetKind,
		Metadata: v1beta1.ObjectMeta{
			Name:   &testFleetName,
			Labels: &map[string]string{},
		},
	}

	// Add test label to fleet metadata
	h.addTestLabelToResource(&testFleet.Metadata)

	switch spec := fleetSpecOrSelector.(type) {
	case v1beta1.FleetSpec:
		testFleet.Spec = spec

	case v1beta1.LabelSelector:

		if len(deviceSpec) == 0 {
			return fmt.Errorf("DeviceSpec is required when using LabelSelector")
		}

		testFleet.Spec = v1beta1.FleetSpec{
			Selector: &spec,
			Template: struct {
				Metadata *v1beta1.ObjectMeta "json:\"metadata,omitempty\""
				Spec     v1beta1.DeviceSpec  "json:\"spec\""
			}{
				Spec: deviceSpec[0],
			},
		}

	default:
		return fmt.Errorf("first parameter must be either FleetSpec or LabelSelector")
	}

	_, err := h.Client.ReplaceFleetWithResponse(h.Context, testFleetName, testFleet)
	return err
}

// Create a test fleet with a configuration
func (h *Harness) CreateTestFleetWithConfig(testFleetName string, testFleetSelector v1beta1.LabelSelector, configProviderSpec v1beta1.ConfigProviderSpec) error {
	var testFleetSpec = v1beta1.DeviceSpec{
		Config: &[]v1beta1.ConfigProviderSpec{
			configProviderSpec,
		},
	}
	err := h.CreateOrUpdateTestFleet(testFleetName, testFleetSelector, testFleetSpec)
	return err
}

func (h *Harness) DeleteFleet(testFleetName string) error {
	_, err := h.Client.DeleteFleet(h.Context, testFleetName)
	return err
}

// HaveReason returns a type-safe Gomega matcher for the Condition's Reason field.
func HaveReason(expected string) types.GomegaMatcher {
	return WithTransform(
		// This part is checked by the compiler!
		func(c v1beta1.Condition) string { return c.Reason },
		Equal(expected),
	)
}

// HaveStatus returns a type-safe Gomega matcher for the Condition's Status field.
func HaveStatus(expected v1beta1.ConditionStatus) types.GomegaMatcher {
	return WithTransform(
		// This part is also checked by the compiler!
		func(c v1beta1.Condition) v1beta1.ConditionStatus { return c.Status },
		Equal(expected),
	)
}

// cHaveType returns a type-safe Gomega matcher for the Condition's Type field.
func cHaveType(expected v1beta1.ConditionType) types.GomegaMatcher {
	return WithTransform(
		// This part is also checked by the compiler!
		func(c v1beta1.Condition) v1beta1.ConditionType { return c.Type },
		Equal(expected),
	)
}

func (h *Harness) WaitForFleetUpdateToFail(fleetName string) {
	logrus.Infof("Waiting for fleet update to fail for fleet %s", fleetName)
	Eventually(h.GetRolloutStatus, LONGTIMEOUT, POLLING).
		WithArguments(fleetName).
		Should(SatisfyAll(
			cHaveType(v1beta1.ConditionTypeFleetRolloutInProgress),
			HaveStatus(v1beta1.ConditionStatusFalse),
			HaveReason(v1beta1.RolloutSuspendedReason),
		), fmt.Sprintf("Timed out waiting for fleet %s update to fail", fleetName))

}

func (h *Harness) WaitForBatchStart(fleetName string, batchNumber int) {
	Eventually(func() int {
		response, err := h.Client.GetFleetWithResponse(h.Context, fleetName, nil)
		if err != nil {
			GinkgoWriter.Printf("failed to get fleet with response: %s\n", err)
			return -2
		}
		if response == nil {
			GinkgoWriter.Printf("fleet response is nil\n")
			return -2
		}
		fleet := response.JSON200
		if fleet == nil {
			GinkgoWriter.Printf("fleet is nil\n")
			return -2
		}

		annotations := fleet.Metadata.Annotations
		if annotations == nil {
			GinkgoWriter.Printf("annotations are nil\n")
			return -2
		}

		batchNumberStr, ok := (*annotations)[v1beta1.FleetAnnotationBatchNumber]
		if !ok {
			GinkgoWriter.Printf("batch number not found in annotations - available annotations: %v\n", *annotations)
			return -2
		}

		batchNumberInt, err := strconv.Atoi(batchNumberStr)
		if err != nil {
			GinkgoWriter.Printf("failed to convert batch number to int: %s\n", err)
			return -2
		}

		GinkgoWriter.Printf("Current batch number: %d, waiting for  %d\n", batchNumberInt, batchNumber)

		return batchNumberInt
	}, LONGTIMEOUT, POLLINGLONG).Should(Equal(batchNumber))
}

func (h *Harness) GetRolloutStatus(fleetName string) (v1beta1.Condition, error) {
	response, err := h.Client.GetFleetWithResponse(h.Context, fleetName, nil)
	if err != nil {
		return v1beta1.Condition{}, fmt.Errorf("failed to get fleet with response: %s", err)
	}
	fleet := response.JSON200

	if fleet.Status == nil || fleet.Status.Conditions == nil {
		return v1beta1.Condition{}, fmt.Errorf("fleet status or conditions is nil")
	}

	for _, condition := range fleet.Status.Conditions {
		if condition.Type == v1beta1.ConditionTypeFleetRolloutInProgress {
			return condition, nil
		}
	}
	return v1beta1.Condition{}, fmt.Errorf("fleet rollout condition not found")
}

func (h *Harness) UpdateFleetWithRetries(fleetName string, updateFunction func(*v1beta1.Fleet)) {
	updateResourceWithRetries(func() error {
		return h.UpdateFleet(fleetName, updateFunction)
	})
}

func (h *Harness) UpdateFleet(fleetName string, updateFunc func(*v1beta1.Fleet)) error {
	fleet, err := h.GetFleet(fleetName)
	Expect(err).ToNot(HaveOccurred())

	updateFunc(fleet)

	replaceResp, err := h.Client.ReplaceFleetWithResponse(h.Context, fleetName, *fleet)
	if err != nil {
		logrus.Errorf("Unexpected error updating fleet %s: %v", fleetName, err)
		return err
	}

	// response code 200 = updated, we are expecting to update... something else is unexpected
	if replaceResp.StatusCode() != 200 {
		logrus.Errorf("Unexpected http status code received: %d", replaceResp.StatusCode())
		logrus.Errorf("Unexpected http response: %s", string(replaceResp.Body))
		return fmt.Errorf("unexpected status code %d: %s", replaceResp.StatusCode(), string(replaceResp.Body))
	}

	return nil
}
