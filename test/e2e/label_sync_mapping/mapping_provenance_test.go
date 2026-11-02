package label_sync_mapping

import (
	"errors"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Label sync mapping provenance", Serial, Label("integration", "sanity", "mapping-provenance"), func() {
	It("reports exact reservations when a scalar mapping has no output", func() {
		harness := e2e.GetWorkerHarness()
		mappingName := uniqueName(harness, "empty-scalar")
		deviceName := uniqueName(harness, "empty-output-device")
		reservedKey := "e2e.flightctl.io/reserved-" + uniqueName(harness, "key")
		unknownKey := "e2e.flightctl.io/unknown-" + uniqueName(harness, "key")

		By("creating an active scalar mapping that currently evaluates to null")
		createLabelSyncMapping(harness, mappingName, &reservedKey, scopedScalarExpression(harness, "dyn(null)"))
		createDeviceWithStatus(harness, deviceName, map[string]string{"operator": "kept"}, "amd64", "linux", nil)
		waitForMappingReady(harness, mappingName)
		waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "kept"}, reservedKey)

		By("blocking a direct write to the scalar-reserved key while it has no output")
		device := getDevice(harness, deviceName)
		labels := cloneLabels(device.Metadata.Labels)
		labels[reservedKey] = "operator-attempt"
		device.Metadata.Labels = &labels
		blockedWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *device)
		Expect(err).NotTo(HaveOccurred())
		Expect(blockedWrite.StatusCode()).To(Equal(http.StatusConflict), string(blockedWrite.Body))

		By("querying organization provenance for the reserved and unrelated exact keys")
		organizationProvenance, err := harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{reservedKey, unknownKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(organizationProvenance.StatusCode()).To(Equal(http.StatusOK), string(organizationProvenance.Body))
		Expect(organizationProvenance.JSON200).NotTo(BeNil())
		Expect(organizationProvenance.JSON200.Items).To(HaveLen(2))
		Expect(organizationProvenance.JSON200.Items[0].Key).To(Equal(reservedKey))
		Expect(organizationProvenance.JSON200.Items[1].Key).To(Equal(unknownKey))
		Expect(organizationProvenance.JSON200.Items[1].Owners).To(BeEmpty())

		By("confirming that the device has no current value for the reservation")
		deviceProvenance, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(harness.Context, deviceName)
		Expect(err).NotTo(HaveOccurred())
		Expect(deviceProvenance.StatusCode()).To(Equal(http.StatusOK), string(deviceProvenance.Body))
		Expect(deviceProvenance.JSON200).NotTo(BeNil())
		Expect(deviceProvenance.JSON200.Items).To(BeEmpty())

		missingDevice, err := harness.Client.GetDeviceLabelSyncProvenanceWithResponse(
			harness.Context,
			uniqueName(harness, "missing-device"),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(missingDevice.StatusCode()).To(Equal(http.StatusNotFound), string(missingDevice.Body))

		By("rejecting a second scalar mapping that reserves the same exact key")
		duplicate := newLabelSyncMapping(
			harness,
			uniqueName(harness, "duplicate-scalar"),
			&reservedKey,
			scopedScalarExpression(harness, "status.systemInfo.architecture"),
		)
		duplicateResponse, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, duplicate)
		Expect(err).NotTo(HaveOccurred())
		Expect(duplicateResponse.StatusCode()).To(Equal(http.StatusConflict), string(duplicateResponse.Body))

		By("confirming that the active scalar reservation appears in organization provenance")
		Expect(organizationProvenance.JSON200.Items[0].Owners).To(ConsistOf(mappingName))
	})

	It("keeps provenance scoped to the selected organization", func() {
		harness := e2e.GetWorkerHarness()
		clientConfig, err := harness.ReadClientConfig("")
		Expect(err).NotTo(HaveOccurred())
		Expect(clientConfig).NotTo(BeNil())
		originalOrg := clientConfig.Organization
		if originalOrg == "" {
			defer func() {
				configPath, err := client.DefaultFlightctlClientConfigPath()
				Expect(err).NotTo(HaveOccurred())
				latestConfig, err := harness.ReadClientConfig("")
				Expect(err).NotTo(HaveOccurred())
				latestConfig.Organization = ""
				Expect(latestConfig.Persist(configPath)).To(Succeed())
				Expect(harness.RefreshClient()).To(Succeed())
			}()
			originalOrg, err = harness.GetOrganizationID()
			Expect(err).NotTo(HaveOccurred())
			Expect(harness.SetCurrentOrganization(originalOrg)).To(Succeed())
		}
		currentUser, err := harness.GetCurrentUserPreferredUsername()
		Expect(err).NotTo(HaveOccurred())
		providers := setup.GetDefaultProviders()
		Expect(providers).NotTo(BeNil())
		Expect(providers.RBAC).NotTo(BeNil())

		secondOrg := uniqueName(harness, "provenance-org")
		secondOrgID := ""
		mappingName := uniqueName(harness, "org-scoped-map")
		firstOnlyMappingName := uniqueName(harness, "first-only-map")
		deviceName := uniqueName(harness, "org-scoped-device")
		sharedKey := "e2e.flightctl.io/org-scoped-" + uniqueName(harness, "key")
		firstOnlyKey := "e2e.flightctl.io/first-only-" + uniqueName(harness, "key")
		secondOrgCreated := false
		secondMappingCreated := false
		secondDeviceCreated := false
		defer func() {
			if !secondOrgCreated {
				return
			}

			var cleanupErrors []error
			if err := harness.SetCurrentOrganization(secondOrgID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("switch to temporary organization for cleanup: %w", err))
			} else {
				if secondMappingCreated {
					response, err := harness.Client.DeleteLabelSyncMappingWithResponse(harness.Context, mappingName)
					if err != nil {
						cleanupErrors = append(cleanupErrors, fmt.Errorf("delete temporary mapping: %w", err))
					} else if response.StatusCode() != http.StatusOK && response.StatusCode() != http.StatusNotFound {
						cleanupErrors = append(cleanupErrors, fmt.Errorf("delete temporary mapping returned HTTP %d: %s", response.StatusCode(), response.Body))
					}
				}
				if secondDeviceCreated {
					if err := harness.DeleteDeviceIgnoreNotFound(deviceName); err != nil {
						cleanupErrors = append(cleanupErrors, fmt.Errorf("delete temporary device: %w", err))
					}
				}
			}
			if err := harness.SetCurrentOrganization(originalOrg); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("restore original organization: %w", err))
			}
			if err := providers.RBAC.DeleteOrganization(e2e.GetWorkerContext(), secondOrg); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete temporary organization: %w", err))
			}
			Expect(errors.Join(cleanupErrors...)).NotTo(HaveOccurred())
		}()

		By("creating a second organization and granting the current test user access")
		Expect(providers.RBAC.CreateOrganization(e2e.GetWorkerContext(), secondOrg)).To(Succeed())
		secondOrgCreated = true
		Expect(providers.RBAC.AddUserToOrg(e2e.GetWorkerContext(), secondOrg, currentUser)).To(Succeed())
		Eventually(func(g Gomega) {
			resolvedID, err := harness.GetOrganizationIDForNamespace(secondOrg)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(resolvedID).NotTo(BeEmpty())
			secondOrgID = resolvedID
		}).Should(Succeed())

		By("creating a mapping and device with the same key in the original organization")
		createLabelSyncMapping(harness, mappingName, &sharedKey, scopedScalarExpression(harness, "status.systemInfo.architecture"))
		createLabelSyncMapping(harness, firstOnlyMappingName, &firstOnlyKey, scopedScalarExpression(harness, "status.systemInfo.operatingSystem"))
		createDeviceWithStatus(harness, deviceName, map[string]string{"operator": "one"}, "amd64", "linux", nil)
		waitForDeviceLabels(harness, deviceName, map[string]string{sharedKey: "amd64", firstOnlyKey: "linux"})
		waitForMappingReady(harness, mappingName)
		waitForMappingReady(harness, firstOnlyMappingName)

		By("creating the same key in the second organization")
		Expect(harness.SetCurrentOrganization(secondOrgID)).To(Succeed())
		Eventually(func(g Gomega) {
			response, err := harness.Client.ListLabelSyncMappingsWithResponse(harness.Context, &api.ListLabelSyncMappingsParams{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(response.StatusCode()).To(Equal(http.StatusOK), string(response.Body))
		}).Should(Succeed())
		secondMapping := newLabelSyncMapping(
			harness,
			mappingName,
			&sharedKey,
			scopedScalarExpression(harness, "status.systemInfo.architecture"),
		)
		createResponse, err := harness.Client.CreateLabelSyncMappingWithResponse(harness.Context, secondMapping)
		Expect(err).NotTo(HaveOccurred())
		Expect(createResponse.StatusCode()).To(Equal(http.StatusCreated), string(createResponse.Body))
		secondMappingCreated = true
		secondDeviceCreated = true
		createDeviceWithStatus(harness, deviceName, map[string]string{"operator": "two"}, "arm64", "linux", nil)
		waitForDeviceLabels(harness, deviceName, map[string]string{sharedKey: "arm64"})
		waitForMappingReady(harness, mappingName)
		waitForDeviceProvenance(harness, deviceName, sharedKey, mappingName)

		secondOrgProvenance, err := harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{sharedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondOrgProvenance.StatusCode()).To(Equal(http.StatusOK), string(secondOrgProvenance.Body))
		Expect(ownersForKey(secondOrgProvenance.JSON200.Items, sharedKey)).To(ConsistOf(mappingName))
		secondOrgFirstOnly, err := harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{firstOnlyKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondOrgFirstOnly.StatusCode()).To(Equal(http.StatusOK), string(secondOrgFirstOnly.Body))
		Expect(ownersForKey(secondOrgFirstOnly.JSON200.Items, firstOnlyKey)).To(BeEmpty())

		By("confirming writes to the mapping-owned key are protected in the second organization")
		secondDevice := getDevice(harness, deviceName)
		secondLabels := cloneLabels(secondDevice.Metadata.Labels)
		secondLabels[sharedKey] = "forged"
		secondDevice.Metadata.Labels = &secondLabels
		blockedSecondOrgWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *secondDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(blockedSecondOrgWrite.StatusCode()).To(Equal(http.StatusConflict), string(blockedSecondOrgWrite.Body))

		By("verifying that the original organization only sees its own mapping")
		Expect(harness.SetCurrentOrganization(originalOrg)).To(Succeed())
		originalOrgProvenance, err := harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{sharedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(originalOrgProvenance.StatusCode()).To(Equal(http.StatusOK), string(originalOrgProvenance.Body))
		Expect(ownersForKey(originalOrgProvenance.JSON200.Items, sharedKey)).To(ConsistOf(mappingName))
		originalFirstOnly, err := harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{firstOnlyKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(originalFirstOnly.StatusCode()).To(Equal(http.StatusOK), string(originalFirstOnly.Body))
		Expect(ownersForKey(originalFirstOnly.JSON200.Items, firstOnlyKey)).To(ConsistOf(firstOnlyMappingName))
		waitForDeviceProvenance(harness, deviceName, sharedKey, mappingName)

		By("confirming the original organization still protects its own mapping key")
		firstDevice := getDevice(harness, deviceName)
		firstLabels := cloneLabels(firstDevice.Metadata.Labels)
		firstLabels[sharedKey] = "forged"
		firstDevice.Metadata.Labels = &firstLabels
		blockedOriginalOrgWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *firstDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(blockedOriginalOrgWrite.StatusCode()).To(Equal(http.StatusConflict), string(blockedOriginalOrgWrite.Body))

		By("deleting the second organization's mapping without affecting the original")
		Expect(harness.SetCurrentOrganization(secondOrgID)).To(Succeed())
		deleteLabelSyncMapping(harness, mappingName)
		waitForMappingDeleted(harness, mappingName)
		secondMappingCreated = false
		waitForDeviceLabels(harness, deviceName, map[string]string{"operator": "two"}, sharedKey)
		secondOrgProvenance, err = harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{sharedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondOrgProvenance.StatusCode()).To(Equal(http.StatusOK), string(secondOrgProvenance.Body))
		Expect(ownersForKey(secondOrgProvenance.JSON200.Items, sharedKey)).To(BeEmpty())
		secondDevice = getDevice(harness, deviceName)
		secondLabels = cloneLabels(secondDevice.Metadata.Labels)
		secondLabels[sharedKey] = "operator-after-delete"
		secondDevice.Metadata.Labels = &secondLabels
		releasedSecondOrgWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *secondDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(releasedSecondOrgWrite.StatusCode()).To(Equal(http.StatusOK), string(releasedSecondOrgWrite.Body))
		waitForDeviceLabels(harness, deviceName, map[string]string{sharedKey: "operator-after-delete"})

		Expect(harness.SetCurrentOrganization(originalOrg)).To(Succeed())
		originalMapping, err := harness.Client.GetLabelSyncMappingWithResponse(harness.Context, mappingName)
		Expect(err).NotTo(HaveOccurred())
		Expect(originalMapping.StatusCode()).To(Equal(http.StatusOK), string(originalMapping.Body))
		waitForDeviceLabels(harness, deviceName, map[string]string{sharedKey: "amd64", firstOnlyKey: "linux"})
		originalOrgProvenance, err = harness.Client.GetLabelSyncProvenanceWithResponse(
			harness.Context,
			&api.GetLabelSyncProvenanceParams{LabelKeys: []string{sharedKey}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(originalOrgProvenance.StatusCode()).To(Equal(http.StatusOK), string(originalOrgProvenance.Body))
		Expect(ownersForKey(originalOrgProvenance.JSON200.Items, sharedKey)).To(ConsistOf(mappingName))
		waitForDeviceProvenance(harness, deviceName, sharedKey, mappingName)
		firstDevice = getDevice(harness, deviceName)
		firstLabels = cloneLabels(firstDevice.Metadata.Labels)
		firstLabels[sharedKey] = "forged-after-other-org-delete"
		firstDevice.Metadata.Labels = &firstLabels
		stillProtectedWrite, err := harness.Client.ReplaceDeviceWithResponse(harness.Context, deviceName, *firstDevice)
		Expect(err).NotTo(HaveOccurred())
		Expect(stillProtectedWrite.StatusCode()).To(Equal(http.StatusConflict), string(stillProtectedWrite.Body))
	})
})
