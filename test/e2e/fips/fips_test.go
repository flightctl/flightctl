// FIPS verification e2e tests based on OpenShift FIPS readiness document section 4.1
// (Functional Tests - FIPS verification). Requires OpenShift deployment with FIPS enabled.

package fips_test

import (
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	fipsTestTimeout  = 2 * time.Minute
	fipsTestPolling  = 2 * time.Second
	fipsRepoBaseName = "fips-private-repo"
)

var _ = Describe("FIPS verification", Label("fips"), func() {

	// 88250 - FIPS enablement checks
	// Step: Check on OpenShift deployment that fips is enabled with
	//   oc get cm cluster-config-v1 -n kube-system -o jsonpath='{.data.install-config}' | grep -i fips
	// Expected: fips=true
	It("cluster has FIPS enabled in install-config", Label("88250"), func() {
		harness := e2e.GetWorkerHarness()

		By("getting cluster install-config from cluster-config-v1")
		out, err := harness.SH("oc", "get", "cm", "cluster-config-v1", "-n", "kube-system", "-o", "jsonpath={.data.install-config}")
		if err != nil {
			// ConfigMap may not exist on non-OCP or older clusters
			GinkgoWriter.Printf("oc get cluster-config-v1 failed (cluster may not be OCP or FIPS-capable): %v\n", err)
			Skip("cluster-config-v1 not available or oc failed: " + err.Error())
		}

		By("verifying install-config contains fips=true")
		Expect(out).To(ContainSubstring("fips"), "install-config should mention fips")
		Expect(strings.ToLower(out)).To(ContainSubstring("fips=true"), "FIPS must be enabled (fips=true) for FIPS readiness")
	})

	// 88252 - FIPS private repo creation
	// Step: Create a private repo; add repo to flightctl via UI with private key.
	// Expected: Private repo should add all the machines and be on green (repository accessible).
	// This test uses the API with SSH credentials (equivalent to adding via UI with private key).
	It("private repo with SSH credentials becomes accessible", Label("88252"), func() {
		harness := e2e.GetWorkerHarness()
		ctx := harness.Context
		svc := auxiliary.Get(ctx)
		if svc == nil || svc.GitServer == nil {
			Skip("git server not available for private repo test")
		}
		gs := svc.GitServer

		config := e2e.GitServerConfig{
			Host: gs.Host,
			Port: gs.Port,
			User: "user",
		}
		keyPath, err := svc.GetGitSSHPrivateKeyPath()
		Expect(err).ToNot(HaveOccurred())
		keyContent, err := svc.GetGitSSHPrivateKey()
		Expect(err).ToNot(HaveOccurred())

		repoName := fipsRepoBaseName + "-" + harness.GetTestIDFromContext()
		internalHost := gs.InternalHost
		internalPort := gs.InternalPort

		By("creating a private repo on the e2e git server")
		err = harness.CreateGitRepositoryOnServer(config, keyPath, repoName)
		Expect(err).ToNot(HaveOccurred())
		defer func() {
			_ = harness.DeleteGitRepositoryOnServer(config, keyPath, repoName)
			_ = harness.DeleteRepository(repoName)
		}()

		By("adding the repo to flightctl with SSH credentials (private key)")
		err = harness.CreateRepositoryWithValidE2ECredentials(internalHost, internalPort, repoName, keyContent)
		Expect(err).ToNot(HaveOccurred())

		By("waiting for repository to become accessible (green)")
		err = harness.WaitForRepositoryAccessible(repoName, fipsTestTimeout, fipsTestPolling)
		Expect(err).ToNot(HaveOccurred(), "private repo with SSH key should become accessible")
	})

	// 88251 - FIPS agent enrollment
	// Step: Create a VM with FIPS enabled (01-fips.toml with kargs = ["fips=1"],
	//       containerfile: COPY 01-fips.toml /usr/lib/bootc/kargs.d/, dnf install crypto-policies, set FIPS).
	//       Enroll FIPS-enabled VM.
	// Expected: VM has FIPS enabled and enrollment request is created; VM is correctly enrolled.
	// This test is skipped unless a FIPS-enabled agent image/VM is provided (see README).
	It("FIPS-enabled VM can enroll", Label("88251"), func() {
		if os.Getenv("E2E_FIPS_VM") != "1" {
			Skip("88251 requires a FIPS-enabled VM image. Set E2E_FIPS_VM=1 and use an agent image built with FIPS (01-fips.toml, crypto-policies). See test/e2e/fips/README.md")
		}
		// When E2E_FIPS_VM=1, the test runner is expected to provide a FIPS-enabled VM pool or image.
		// Here we only run enrollment flow if the harness has a VM (same as agent suite).
		harness := e2e.GetWorkerHarness()
		workerID := GinkgoParallelProcess()
		err := harness.SetupVMFromPoolAndStartAgent(workerID)
		if err != nil {
			Skip("FIPS VM pool or agent not available: " + err.Error())
		}
		enrollmentID := harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
		Expect(enrollmentID).NotTo(BeEmpty())
		_ = harness.WaitForEnrollmentRequest(enrollmentID)
		harness.ApproveEnrollment(enrollmentID, harness.TestEnrollmentApproval())
		Eventually(harness.GetDeviceWithStatusSystem, fipsTestTimeout, fipsTestPolling).WithArguments(enrollmentID).ShouldNot(BeNil())
	})
})
