package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccoveille/go-safecast"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/pkg/certmanager"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/test/harness"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	TIMEOUT = "60s"
	POLLING = "250ms"
)

var (
	suiteCtx context.Context
)

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Agent Suite")
})

var _ = Describe("Device Agent behavior", func() {
	var (
		ctx context.Context
		h   *harness.TestHarness
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)

		var err error
		h, err = harness.NewTestHarness(ctx, GinkgoT().TempDir(), func(err error) {
			// this inline function handles any errors that are returned from go routines
			fmt.Fprintf(os.Stderr, "Error in test harness go routine: %v\n", err)
			GinkgoWriter.Printf("Error in go routine: %v\n", err)
			GinkgoRecover()
		})
		// check for test harness creation errors
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		h.Cleanup()
	})

	Context("enrollment", func() {
		It("should submit a request for enrollment", func() {
			deviceName := ""
			Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
		})

		When("an enrollment request is approved", func() {
			It("should mark enrollment request as approved", func() {
				deviceName := ""
				Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
				approveEnrollment(h, deviceName, testutil.TestEnrollmentApproval())

				// verify that the enrollment request is marked as approved
				er, status := h.ServiceHandler.GetEnrollmentRequest(h.AuthenticatedContext(h.Context), org.DefaultID, deviceName)
				Expect(status.Code).To(BeEquivalentTo(200))
				Expect(er.Status.Conditions).ToNot(BeEmpty())

				Expect(v1beta1.IsStatusConditionTrue(er.Status.Conditions, "Approved")).To(BeTrue())

			})

			It("should create a device", func() {
				dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())
				Expect(dev.Metadata.Name).NotTo(BeNil())
			})

			It("should create a device, with the approval labels", func() {
				// craft some specific labels we will test for in the device
				approval := testutil.TestEnrollmentApproval()
				const (
					TEST_LABEL_1 = "label-1"
					TEST_VALUE_1 = "value-1"
					TEST_LABEL_2 = "label-2"
					TEST_VALUE_2 = "value-2"
				)
				approval.Labels = &map[string]string{TEST_LABEL_1: TEST_VALUE_1, TEST_LABEL_2: TEST_VALUE_2}

				dev := enrollAndWaitForDevice(h, approval)

				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue(TEST_LABEL_1, TEST_VALUE_1))
				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue(TEST_LABEL_2, TEST_VALUE_2))
			})

			It("should write the agent.crt to the device", func() {
				dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

				GinkgoWriter.Printf(
					"Waiting for agent.crt file to be created on the device %s, with testDirPath: %s\n",
					*dev.Metadata.Name, h.TestDirPath)

				var fileInfo fs.FileInfo
				Eventually(func() bool {
					var err error
					fileInfo, err = os.Stat(filepath.Join(h.TestDirPath, "/var/lib/flightctl/certs/agent.crt"))
					if err != nil && os.IsNotExist(err) {
						return false
					}
					return true
				}, TIMEOUT, POLLING).Should(BeTrue())

				Expect(fileInfo.Mode()).To(Equal(os.FileMode(0600)))
			})

		})

		When("updating the agent device spec", func() {
			It("should write any files to the device", func(ctx context.Context) {
				const (
					firstSecretKey    = "first-secret"
					firstSecretValue  = "This is the first secret"
					secondSecretKey   = "second-secret"
					secondSecretValue = "Second secret"
				)
				secrets := map[string]string{
					firstSecretKey:  firstSecretValue,
					secondSecretKey: secondSecretValue,
				}
				mockSecret(h.GetMockK8sClient(), secrets)
				fleet := getTestFleet("fleet.yaml")
				_, status := h.ServiceHandler.CreateFleet(h.AuthenticatedContext(h.Context), org.DefaultID, fleet)
				Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
				approval := testutil.TestEnrollmentApproval()
				approval.Labels = &map[string]string{"fleet": "default"}

				dev := enrollAndWaitForDevice(h, approval)

				waitForFile("/var/lib/flightctl/certs/agent.crt", *dev.Metadata.Name, h.TestDirPath, nil, nil)
				waitForFile("/etc/motd", *dev.Metadata.Name, h.TestDirPath, lo.ToPtr("This system is managed by flightctl."), lo.ToPtr(0o0600))
				waitForFile("/etc/testdir/encoded", *dev.Metadata.Name, h.TestDirPath, lo.ToPtr("This text is encoded."), lo.ToPtr(0o1775))

				for key, value := range secrets {
					fname := filepath.Join("/etc/secret/secretMountPath", key)
					waitForFile(fname, *dev.Metadata.Name, h.TestDirPath, &value, lo.ToPtr(0644))
				}
			})
		})

		When("agent begins enrollment, and the enrollment is approved while the device is shutdown", func() {
			It("the agent should start and complete enrollment successfully", func() {
				deviceName := ""
				Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())

				// shut down the agent before approving the enrollment request
				GinkgoWriter.Printf("Agent has requested enrollment: %s, shutting it down\n", deviceName)
				h.StopAgent()

				// while the agent is down, we approve the enrollment for the device
				approveEnrollment(h, deviceName, testutil.TestEnrollmentApproval())

				// start the agent again
				h.StartAgent()

				// wait for the agent to retrieve the agent certificate from the EnrollmentRequest
				Eventually(h.AgentDownloadedCertificate, TIMEOUT, POLLING).Should(BeTrue())
			})
		})

		When("rotating the device management certificate", func() {
			It("renews the cert and switches to the renewal signer", func(ctx context.Context) {
				const (
					mgmtCertTTLSecondsEnv         = "FLIGHTCTL_TEST_MGMT_CERT_EXPIRY_SECONDS"
					mgmtCertRenewBeforeSecondsEnv = "FLIGHTCTL_TEST_MGMT_CERT_RENEW_BEFORE_SECONDS"
					agentCertRelPath              = "var/lib/flightctl/certs/agent.crt"

					// Keep the test fast:
					// - cert lifetime: 10m
					// - renewBefore: 10m-1s => renewal condition becomes true almost immediately
					testCertTTLSeconds     = 10 * 60
					testRenewBeforeSeconds = testCertTTLSeconds - 1

					expectedRenewalSignerName = "flightctl.io/device-management-renewal"

					// Issuance time jitter only (avoid flakes).
					ttlTolerance = 10 * time.Second
				)

				defer testutil.TestTempEnv(
					mgmtCertTTLSecondsEnv, strconv.Itoa(testCertTTLSeconds),
					mgmtCertRenewBeforeSecondsEnv, strconv.Itoa(testRenewBeforeSeconds),
				)()

				// Enroll device and wait until the agent fetches its initial cert.
				dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())
				deviceName := lo.FromPtr(dev.Metadata.Name)
				Eventually(h.AgentDownloadedCertificate, TIMEOUT, POLLING).Should(BeTrue())

				// Sanity: enrollment approved and server returned an initial certificate.
				er, status := h.ServiceHandler.GetEnrollmentRequest(
					h.AuthenticatedContext(h.Context),
					org.DefaultID,
					deviceName,
				)
				Expect(status.Code).To(BeEquivalentTo(200))
				Expect(v1beta1.IsStatusConditionTrue(er.Status.Conditions, "Approved")).To(BeTrue())
				Expect(er.Status.Certificate).ToNot(BeNil())

				// Assert TTL override took effect (~10m).
				initialCertPEM := lo.FromPtr(er.Status.Certificate)
				initialCert, err := fccrypto.ParsePEMCertificate([]byte(initialCertPEM))
				Expect(err).ToNot(HaveOccurred())

				expectedTTL := time.Duration(testCertTTLSeconds) * time.Second
				actualTTL := initialCert.NotAfter.Sub(initialCert.NotBefore)
				Expect(actualTTL).To(BeNumerically(">", expectedTTL-ttlTolerance))
				Expect(actualTTL).To(BeNumerically("<", expectedTTL+ttlTolerance))

				agentCertPath := filepath.Join(h.TestDirPath, agentCertRelPath)
				GinkgoWriter.Printf("Waiting for device %s to renew certificate (path=%s)\n", deviceName, agentCertPath)

				// Wait until the on-disk agent cert is renewed (signer-name extension flips).
				var renewedCert *x509.Certificate
				Eventually(func() string {
					agentCertPEM, err := os.ReadFile(agentCertPath)
					Expect(err).ToNot(HaveOccurred())

					agentCert, err := fccrypto.ParsePEMCertificate(agentCertPEM)
					Expect(err).ToNot(HaveOccurred())
					renewedCert = agentCert

					signerName, err := signer.GetSignerNameExtension(agentCert)
					Expect(err).ToNot(HaveOccurred())
					return signerName
				}, TIMEOUT, POLLING).Should(Equal(expectedRenewalSignerName))

				// Expected status values are derived from the renewed cert.
				Expect(renewedCert.SerialNumber).ToNot(BeNil())

				serialBytes := renewedCert.SerialNumber.Bytes()
				Expect(serialBytes).ToNot(BeEmpty())

				serialHex := make([]byte, 0, len(serialBytes)*3-1)
				for i, v := range serialBytes {
					if i > 0 {
						serialHex = append(serialHex, ':')
					}
					serialHex = append(serialHex, fmt.Sprintf("%02X", v)...)
				}
				expectedRenewedSerial := string(serialHex)
				expectedNotAfter := renewedCert.NotAfter.UTC().Format(time.RFC3339)

				// Verify device status reflects the renewed cert (serial + notAfter).
				GinkgoWriter.Printf("Waiting for device %s systemInfo fields to update\n", deviceName)

				Eventually(func() []string {
					dev, status := h.ServiceHandler.GetDevice(
						h.AuthenticatedContext(h.Context),
						org.DefaultID,
						deviceName,
					)
					Expect(status.Code).To(BeEquivalentTo(200))

					props := dev.Status.SystemInfo.AdditionalProperties
					return []string{
						props["managementCertSerial"],
						props["managementCertNotAfter"],
					}
				}, TIMEOUT, POLLING).Should(Equal([]string{expectedRenewedSerial, expectedNotAfter}))
			})
		})

		When("provisioning a CSR-managed certificate", func() {
			It("auto-issues cert and writes files; validates CN and extensions", func(ctx context.Context) {
				const (
					certCNPrefix   = "test-certificate"
					certSignerName = "flightctl.io/device-svc-client"
				)

				// prepare cert-manager config (DropInConfigProvider reads /etc/flightctl/certs.yaml)
				csrCfg := map[string]any{
					"signer":      certSignerName,
					"common-name": fmt.Sprintf("%s-{{.DEVICE_ID}}", certCNPrefix),
				}
				csrRaw, err := json.Marshal(csrCfg)
				Expect(err).ToNot(HaveOccurred())

				storageCfg := map[string]any{
					"cert-path": "var/lib/flightctl/certs/test.crt",
					"key-path":  "var/lib/flightctl/certs/test.key",
				}
				storageRaw, err := json.Marshal(storageCfg)
				Expect(err).ToNot(HaveOccurred())

				certcfg := certmanager.CertificateConfig{
					Name: certCNPrefix,
					Provisioner: certmanager.ProvisionerConfig{
						Type:   provider.ProvisionerTypeCSR,
						Config: csrRaw,
					},
					Storage: certmanager.StorageConfig{
						Type:   provider.StorageTypeFilesystem,
						Config: storageRaw,
					},
				}

				cfgBytes, err := yaml.Marshal([]certmanager.CertificateConfig{certcfg})
				Expect(err).ToNot(HaveOccurred())

				certsYaml := filepath.Join(h.TestDirPath, "etc", "flightctl", "certs.yaml")
				Expect(os.MkdirAll(filepath.Dir(certsYaml), 0o755)).To(Succeed())
				Expect(os.WriteFile(certsYaml, cfgBytes, 0o600)).To(Succeed())

				dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())
				deviceName := lo.FromPtr(dev.Metadata.Name)

				// wait for CSR to be created by agent
				var csr v1beta1.CertificateSigningRequest
				Eventually(func() bool {
					list, status := h.ServiceHandler.ListCertificateSigningRequests(h.AuthenticatedContext(h.Context), org.DefaultID, v1beta1.ListCertificateSigningRequestsParams{})
					if status.Code != int32(200) || list == nil {
						return false
					}
					for _, item := range list.Items {
						if item.Spec.SignerName == certSignerName && item.Metadata.Name != nil {
							name := lo.FromPtr(item.Metadata.Name)
							if strings.HasPrefix(name, certCNPrefix+"-") && item.Status != nil && item.Status.Certificate != nil {
								csr = item
								return true
							}
						}
					}
					return false
				}, TIMEOUT, POLLING).Should(BeTrue())

				certBytes := lo.FromPtr(csr.Status.Certificate)
				parsedCert, err := fccrypto.ParsePEMCertificate(certBytes)
				Expect(err).ToNot(HaveOccurred())

				// CN should equal template with device id
				expectedCN := fmt.Sprintf("%s-%s", certCNPrefix, deviceName)
				Expect(parsedCert.Subject.CommonName).To(Equal(expectedCN))

				// signer name extension should match device-svc-client
				signerName, err := signer.GetSignerNameExtension(parsedCert)
				Expect(err).ToNot(HaveOccurred())
				Expect(signerName).To(Equal(certSignerName))

				// fingerprint extension must match the suffix of CN after last '-'
				idx := strings.LastIndex(parsedCert.Subject.CommonName, "-")
				Expect(idx).To(BeNumerically(">", 0))
				cnFingerprint := parsedCert.Subject.CommonName[idx+1:]
				fpExt, err := signer.GetDeviceFingerprintExtension(parsedCert)
				Expect(err).ToNot(HaveOccurred())
				Expect(fpExt).To(Equal(cnFingerprint))

				//wait for certificate files to be written by agent
				waitForFile("var/lib/flightctl/certs/test.crt", deviceName, h.TestDirPath, nil, lo.ToPtr(0o644))
				waitForFile("var/lib/flightctl/certs/test.key", deviceName, h.TestDirPath, nil, lo.ToPtr(0o600))
			})
		})

	})
})

func enrollAndWaitForDevice(h *harness.TestHarness, approval *v1beta1.EnrollmentRequestApproval) *v1beta1.Device {
	deviceName := ""
	Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
	approveEnrollment(h, deviceName, approval)

	// verify that the device is created
	dev, status := h.ServiceHandler.GetDevice(h.AuthenticatedContext(h.Context), org.DefaultID, deviceName)
	Expect(status.Code).To(BeEquivalentTo(200))
	return dev
}

func approveEnrollment(h *harness.TestHarness, deviceName string, approval *v1beta1.EnrollmentRequestApproval) {
	Expect(approval).NotTo(BeNil())
	GinkgoWriter.Printf("Approving device enrollment: %s\n", deviceName)

	// Approve
	_, status := h.ServiceHandler.ApproveEnrollmentRequest(h.AuthenticatedContext(h.Context), org.DefaultID, deviceName, *approval)
	Expect(status.Code).To(BeEquivalentTo(200))
}

func getEnrollmentDeviceName(h *harness.TestHarness, deviceName *string) bool {
	list, status := h.ServiceHandler.ListEnrollmentRequests(h.AuthenticatedContext(h.Context), org.DefaultID, v1beta1.ListEnrollmentRequestsParams{})
	Expect(status.Code).To(BeEquivalentTo(200))

	if list == nil || len(list.Items) == 0 {
		return false
	}

	Expect(*list.Items[0].Metadata.Name).ToNot(BeEmpty())
	*deviceName = *list.Items[0].Metadata.Name

	return true
}

func getTestFleet(fleetYaml string) v1beta1.Fleet {
	fleetBytes, err := os.ReadFile(filepath.Join("testdata", fleetYaml))
	Expect(err).ToNot(HaveOccurred())

	u, err := user.Current()
	Expect(err).ToNot(HaveOccurred())
	fleetBytes = bytes.ReplaceAll(fleetBytes, []byte("_CURRENT_USER_"), []byte(u.Username))
	fleetBytes = bytes.ReplaceAll(fleetBytes, []byte("_CURRENT_GROUP_"), []byte("'"+u.Gid+"'"))

	var fleet v1beta1.Fleet
	err = yaml.Unmarshal(fleetBytes, &fleet)
	Expect(err).ToNot(HaveOccurred())

	return fleet
}

func mockSecret(mockK8sClient *k8sclient.MockK8SClient, secrets map[string]string) {
	mockK8sClient.EXPECT().GetSecret(gomock.Any(), "secret-namespace", "secret").
		Return(&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret",
				Namespace: "secret-namespace",
			},
			Data: lo.MapValues(secrets, func(v, _ string) []byte {
				return []byte(v)
			}),
		}, nil).AnyTimes()
}

func waitForFile(path, devName, testDirPath string, contents *string, mode *int) {
	var fileInfo fs.FileInfo
	var err error

	GinkgoWriter.Printf(
		"Waiting for file %s to be created on the device %s, with testDirPath: %s\n", path, devName, testDirPath)
	Eventually(func() bool {
		fileInfo, err = os.Stat(filepath.Join(testDirPath, path))
		if err != nil && os.IsNotExist(err) {
			return false
		}
		return true
	}, TIMEOUT, POLLING).Should(BeTrue())

	Expect(fileInfo.IsDir()).To(Equal(false))

	if mode != nil {
		filemode, err := safecast.ToUint32(*mode)
		Expect(err).To(BeNil())
		Expect(fileInfo.Mode().Perm()).To(Equal(os.FileMode(filemode).Perm()))
		if *mode&0o1000 != 0 {
			Expect(fileInfo.Mode() & os.ModeSticky).ToNot(Equal(0))
		}
		if *mode&0o2000 != 0 {
			Expect(fileInfo.Mode() & os.ModeSetgid).ToNot(Equal(0))
		}
		if *mode&0o4000 != 0 {
			Expect(fileInfo.Mode() & os.ModeSetuid).ToNot(Equal(0))
		}
	}
	if contents != nil {
		Expect(os.ReadFile(filepath.Join(testDirPath, path))).To(Equal([]byte(*contents)))
	}
}
