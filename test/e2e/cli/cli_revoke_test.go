package cli_test

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	svcconfig "github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"sigs.k8s.io/yaml"
)

const (
	revokeReason        = "e2e enrollment credential compromised"
	revokeSimulatorUser = "revoke-e2e"
)

var submittedCSRNamePattern = regexp.MustCompile(`Submitting certificate signing request "([^"]+)"`)

var _ = Describe("enrollment credential revocation", Label("client"), func() {
	var simulators []*exec.Cmd

	BeforeEach(func() {
		simulators = nil
	})

	AfterEach(func() {
		harness := e2e.GetWorkerHarness()
		for index, simulator := range simulators {
			pid := 0
			if simulator != nil && simulator.Process != nil {
				pid = simulator.Process.Pid
			}
			if err := harness.StopDeviceSimulator(simulator, 30*time.Second); err != nil {
				if isExpectedSimulatorTermination(err) {
					GinkgoWriter.Printf("revocation simulator %d (pid %d) terminated as expected: %v\n", index, pid, err)
					continue
				}
				GinkgoWriter.Printf("stopping revocation simulator %d (pid %d) failed: %v\n", index, pid, err)
			}
		}
		simulators = nil
	})

	It("revokes an issued enrollment credential via the CLI", Label("sanity"), func() {
		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()

		By("creating and issuing an enrollment credential CSR")
		csr := CreateTestCSR()
		csr.Metadata.Labels = &map[string]string{"test-id": testID}
		csrYAML, err := yaml.Marshal(&csr)
		Expect(err).NotTo(HaveOccurred())
		out, err := harness.CLIWithStdin(string(csrYAML), "apply", "-f", "-")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(csr.Metadata.Name).NotTo(BeNil())
		csrName := *csr.Metadata.Name

		Eventually(func() error {
			resp, err := harness.Client.GetCertificateSigningRequestWithResponse(harness.Context, csrName)
			if err != nil {
				return err
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected status %d", resp.StatusCode())
			}
			if resp.JSON200.Status == nil || resp.JSON200.Status.Certificate == nil || len(*resp.JSON200.Status.Certificate) == 0 {
				return fmt.Errorf("enrollment certificate not issued yet")
			}
			return nil
		}, TIMEOUT, POLLING).Should(Succeed())

		By("showing the revoke command help")
		out, err = harness.CLI("revoke", "-h")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("Revoke an issued enrollment credential."))
		Expect(out).To(ContainSubstring("flightctl revoke (TYPE/NAME | TYPE NAME)"))

		By("rejecting revoke without a reason")
		out, err = harness.CLI("revoke", fmt.Sprintf("csr/%s", csrName))
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring(`required flag(s) "reason" not set`))

		By("rejecting resources other than a CSR")
		out, err = harness.CLI("revoke", "device/"+testID, "--reason", revokeReason)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("kind must be certificatesigningrequest"))

		By("reporting a missing CSR")
		out, err = harness.CLI("revoke", "csr/does-not-exist", "--reason", revokeReason)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("404"))

		By("revoking the credential")
		out, err = harness.CLI("revoke", fmt.Sprintf("csr/%s", csrName), "--reason", revokeReason)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring(fmt.Sprintf("Revoked certificate signing request %q", csrName)))

		By("recording the immutable revocation on the CSR")
		var firstRevocation *v1beta1.EnrollmentCertificateRevocation
		Eventually(func() error {
			resp, err := harness.Client.GetCertificateSigningRequestWithResponse(harness.Context, csrName)
			if err != nil {
				return err
			}
			if resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Revocation == nil {
				return fmt.Errorf("revocation not recorded yet")
			}
			revocation := resp.JSON200.Status.Revocation
			if revocation.Reason != revokeReason {
				return fmt.Errorf("unexpected reason %q", revocation.Reason)
			}
			if revocation.RevokedBy == "" {
				return fmt.Errorf("revokedBy is empty")
			}
			if revocation.RevokedAt.IsZero() {
				return fmt.Errorf("revokedAt is zero")
			}
			firstRevocation = revocation
			return nil
		}, TIMEOUT, POLLING).Should(Succeed())

		By("publishing an enrollment credential revocation event")
		Eventually(func() (bool, error) {
			events, err := harness.ListEventsByReason(string(v1beta1.EventReasonEnrollmentCredentialRevoked))
			if err != nil {
				return false, err
			}
			for _, event := range events {
				if event.InvolvedObject.Name == csrName && event.InvolvedObject.Kind == v1beta1.CertificateSigningRequestKind {
					return true, nil
				}
			}
			return false, nil
		}, TIMEOUT, POLLING).Should(BeTrue())

		By("accepting a repeated revocation without changing the original state")
		out, err = harness.CLI("revoke", fmt.Sprintf("csr/%s", csrName), "--reason", "different reason")
		Expect(err).NotTo(HaveOccurred(), out)
		resp, err := harness.Client.GetCertificateSigningRequestWithResponse(harness.Context, csrName)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.JSON200).NotTo(BeNil())
		Expect(resp.JSON200.Status.Revocation).NotTo(BeNil())
		Expect(resp.JSON200.Status.Revocation.Reason).To(Equal(revokeReason))
		Expect(resp.JSON200.Status.Revocation.RevokedAt).To(Equal(firstRevocation.RevokedAt))
	})

	It("denies pending requests and flags admitted devices after revocation", Label("sanity"), func() {
		harness := e2e.GetWorkerHarness()
		testID := harness.GetTestIDFromContext()

		By("creating an enrollment credential for the simulated devices")
		csrName, configPath := createEnrollmentCredentialForRevocation(harness, testID)

		simulators = make([]*exec.Cmd, 0, 2)

		By("enrolling a device with the credential")
		simulators = append(simulators, startRevocationSimulator(harness, testID, configPath, 0))
		enrolledER := waitForSimulatorEnrollmentRequest(harness, testID, 0)
		status, err := harness.ApproveEnrollmentRequestWithLabels(enrolledER, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK))
		Eventually(func() (v1beta1.DeviceSummaryStatusType, error) {
			return harness.GetDeviceWithStatusSummary(enrolledER)
		}, TIMEOUT, POLLING).Should(Equal(v1beta1.DeviceSummaryStatusOnline))
		deviceName := enrolledER

		By("leaving a second enrollment request pending with the same credential")
		simulators = append(simulators, startRevocationSimulator(harness, testID, configPath, 1))
		pendingER := waitForSimulatorEnrollmentRequest(harness, testID, 1)

		By("revoking the enrollment credential")
		out, err := harness.CLI("revoke", fmt.Sprintf("csr/%s", csrName), "--reason", revokeReason)
		Expect(err).NotTo(HaveOccurred(), out)

		By("denying the pending enrollment request")
		Eventually(func() error {
			resp, err := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, pendingER)
			if err != nil {
				return err
			}
			if resp.JSON200 == nil || resp.JSON200.Status == nil {
				return fmt.Errorf("enrollment request not readable")
			}
			condition := v1beta1.FindStatusCondition(resp.JSON200.Status.Conditions, v1beta1.ConditionTypeCertificateSigningRequestDenied)
			if condition == nil || condition.Status != v1beta1.ConditionStatusTrue {
				return fmt.Errorf("pending enrollment request is not denied yet")
			}
			if condition.Reason != v1beta1.EnrollmentCredentialRevokedReason {
				return fmt.Errorf("unexpected denial reason %q", condition.Reason)
			}
			return nil
		}, LONG_TIMEOUT, POLLING).Should(Succeed())

		By("flagging the admitted device without evicting it")
		Eventually(func() error {
			resp, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceName)
			if err != nil {
				return err
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("device disappeared after revocation")
			}
			credential := resp.JSON200.Status.EnrollmentCredential
			if credential == nil || !credential.Revoked {
				return fmt.Errorf("device is not flagged as revoked yet")
			}
			condition := v1beta1.FindStatusCondition(resp.JSON200.Status.Conditions, v1beta1.ConditionTypeDeviceEnrollmentCredentialRevoked)
			if condition == nil || condition.Status != v1beta1.ConditionStatusTrue {
				return fmt.Errorf("device revocation condition is not true yet")
			}
			if condition.Reason != v1beta1.EnrollmentCredentialRevokedReason {
				return fmt.Errorf("unexpected device condition reason %q", condition.Reason)
			}
			return nil
		}, LONG_TIMEOUT, LONG_POLLING).Should(Succeed())

		By("recording completion of consequence processing on the CSR")
		var revokedAt time.Time
		Eventually(func() error {
			resp, err := harness.Client.GetCertificateSigningRequestWithResponse(harness.Context, csrName)
			if err != nil {
				return err
			}
			if resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Revocation == nil {
				return fmt.Errorf("revocation not recorded")
			}
			revocation := resp.JSON200.Status.Revocation
			revokedAt = revocation.RevokedAt
			if revocation.FlaggingCompletedAt == nil {
				return fmt.Errorf("flagging has not completed yet")
			}
			return nil
		}, LONG_TIMEOUT, LONG_POLLING).Should(Succeed())
		Expect(revokedAt.IsZero()).To(BeFalse())

		By("listing flagged devices with the revocation field selector")
		out, err = harness.CLI("get", "devices", "--field-selector", "status.enrollmentCredential.revoked=true")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring(deviceName))

		By("keeping the admitted device managed after revocation")
		Eventually(func() (bool, error) {
			summary, err := harness.GetDeviceWithStatusSummary(deviceName)
			if err != nil {
				return false, err
			}
			lastSeenResp, err := harness.Client.GetDeviceLastSeenWithResponse(harness.Context, deviceName)
			if err != nil {
				return false, err
			}
			if lastSeenResp.JSON200 == nil {
				return false, fmt.Errorf("unexpected lastSeen status %d", lastSeenResp.StatusCode())
			}
			return summary == v1beta1.DeviceSummaryStatusOnline && lastSeenResp.JSON200.LastSeen.After(revokedAt), nil
		}, TIMEOUT, POLLING).Should(BeTrue(), "device should remain online and report activity after revocation")

		By("rejecting new enrollment with the revoked credential")
		enrollmentClient := enrollmentClientForCredential(configPath)
		deviceName, csrPEM := GenerateDeviceNameAndCSR()
		createResp, err := enrollmentClient.CreateEnrollmentRequestWithResponse(harness.Context, v1beta1.EnrollmentRequest{
			ApiVersion: v1beta1.EnrollmentRequestAPIVersion,
			Kind:       v1beta1.EnrollmentRequestKind,
			Metadata:   v1beta1.ObjectMeta{Name: lo.ToPtr(deviceName)},
			Spec:       v1beta1.EnrollmentRequestSpec{Csr: string(csrPEM)},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(createResp).NotTo(BeNil())
		Expect(createResp.StatusCode()).To(Equal(http.StatusUnauthorized))
		Expect(createResp.JSON401).NotTo(BeNil())
		Expect(createResp.JSON401.Message).To(ContainSubstring("Enrollment certificate is revoked"))
	})
})

// createEnrollmentCredentialForRevocation requests a unique enrollment certificate and
// prepares an agent config plus the fixed certificate file names expected by the
// device simulator.
func createEnrollmentCredentialForRevocation(harness *e2e.Harness, testID string) (string, string) {
	GinkgoHelper()

	baseDir := GinkgoT().TempDir()
	certDir := filepath.Join(baseDir, "certs")
	Expect(os.MkdirAll(certDir, 0o755)).To(Succeed())

	csrPrefix := "revoke-" + testID[:8]
	out, err := harness.CLI("certificate", "request", "-n", csrPrefix, "-d", certDir, "-o", "reference")
	Expect(err).NotTo(HaveOccurred())

	matches := submittedCSRNamePattern.FindAllStringSubmatch(out, -1)
	Expect(matches).NotTo(BeEmpty(), "certificate request did not report the CSR name")
	csrName := matches[len(matches)-1][1]
	Expect(csrName).NotTo(BeEmpty())
	DeferCleanup(func() {
		if _, err := harness.CleanUpResource(util.CertificateSigningRequest, csrName); err != nil {
			GinkgoWriter.Printf("deleting enrollment credential CSR %s: %v\n", csrName, err)
		}
	})

	configStart := strings.Index(out, "enrollment-service:")
	Expect(configStart).To(BeNumerically(">=", 0), "certificate request did not emit an agent config")
	configYAML := out[configStart:] + "\nspec-fetch-interval: 0m2s\nstatus-update-interval: 0m2s\n"
	configPath := filepath.Join(baseDir, "config.yaml")
	Expect(os.WriteFile(configPath, []byte(configYAML), 0o600)).To(Succeed())

	copyWithMode := func(src, dst string) {
		data, readErr := os.ReadFile(src)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(os.WriteFile(dst, data, 0o600)).To(Succeed())
	}
	copyWithMode(filepath.Join(certDir, csrPrefix+".crt"), filepath.Join(certDir, "client-enrollment.crt"))
	copyWithMode(filepath.Join(certDir, csrPrefix+".key"), filepath.Join(certDir, "client-enrollment.key"))
	return csrName, configPath
}

// enrollmentClientForCredential builds an API client that authenticates with the
// enrollment certificate referenced by the generated agent config.
func enrollmentClientForCredential(configPath string) *apiclient.ClientWithResponses {
	GinkgoHelper()

	raw, err := os.ReadFile(configPath)
	Expect(err).NotTo(HaveOccurred())
	var serviceConfig svcconfig.ServiceConfig
	Expect(yaml.Unmarshal(raw, &serviceConfig)).To(Succeed())

	service := serviceConfig.EnrollmentService.Service
	auth := serviceConfig.EnrollmentService.AuthInfo
	Expect(service.Server).NotTo(BeEmpty())
	configDir := filepath.Dir(configPath)

	clientConfig := &client.Config{
		Service: client.Service{
			Server:               service.Server,
			TLSServerName:        service.TLSServerName,
			CertificateAuthority: filepath.Join(configDir, service.CertificateAuthority),
			InsecureSkipVerify:   service.InsecureSkipVerify,
		},
		AuthInfo: client.AuthInfo{
			ClientCertificate: filepath.Join(configDir, auth.ClientCertificate),
			ClientKey:         filepath.Join(configDir, auth.ClientKey),
		},
	}
	httpClient, err := client.NewHTTPClientForServer(clientConfig, service.Server)
	Expect(err).NotTo(HaveOccurred())

	apiClient, err := apiclient.NewClientWithResponses(
		client.JoinServerURL(service.Server, apiclient.ServerUrlApiv1),
		apiclient.WithHTTPClient(httpClient),
	)
	Expect(err).NotTo(HaveOccurred())
	return apiClient
}

// isExpectedSimulatorTermination reports whether err is the result of the
// SIGTERM that StopDeviceSimulator sends intentionally. Non-zero exits and the
// SIGKILL used after a shutdown timeout are not expected.
func isExpectedSimulatorTermination(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGTERM
}

func startRevocationSimulator(harness *e2e.Harness, testID, configPath string, initialIndex int) *exec.Cmd {
	GinkgoHelper()

	simulator, err := harness.StartLabeledSimulatorWithOptions(harness.Context, testID, revokeSimulatorUser, initialIndex, 1, e2e.StartLabeledSimulatorOptions{
		SkipAutoApprove: true,
		ConfigPath:      configPath,
	})
	Expect(err).NotTo(HaveOccurred())
	return simulator
}

func waitForSimulatorEnrollmentRequest(harness *e2e.Harness, testID string, initialIndex int) string {
	GinkgoHelper()

	enrollmentRequest, err := harness.WaitForSimulatorEnrollmentRequest(testID, e2e.DeviceSimulatorAgentAlias(initialIndex), TIMEOUT, POLLING)
	Expect(err).NotTo(HaveOccurred())
	Expect(enrollmentRequest).NotTo(BeEmpty())
	return enrollmentRequest
}
