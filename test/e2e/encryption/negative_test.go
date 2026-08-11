package encryption_test

import (
	"fmt"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Encryption at rest — Negative", Label("encryption", "negative"), func() {
	var (
		harness   *e2e.Harness
		providers *infra.Providers
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")
	})

	// N3: Sensitive fields are redacted in LIST responses, not just GET-by-name.
	// HideSensitiveData() runs for every response via WriteJSONResponse — this
	// test confirms LIST does not regress to exposing plaintext credentials.
	Context("When sensitive resources are listed via the API", func() {
		It("N3: should redact sensitive fields in LIST responses, not only in GET-by-name",
			Label("90111", "sanity"), func() {

				ctx := harness.GetTestContext()
				testID := harness.GetTestIDFromContext()

				issuerURL := auxSvcs.Keycloak.IssuerURL()
				apName1 := "enc-neg-ap1-" + testID
				apName2 := "enc-neg-ap2-" + testID
				secret1 := "neg-secret-one-" + testID
				secret2 := "neg-secret-two-" + testID
				clientID1 := "enc-neg-c1-" + testID
				clientID2 := "enc-neg-c2-" + testID

				By("creating two AuthProviders with distinct clientSecrets")
				manifest1 := buildOIDCAuthProviderYAML(apName1, issuerURL, clientID1, secret1)
				_, err := applyManifest(harness, manifest1)
				Expect(err).ToNot(HaveOccurred(), "create first AuthProvider")

				manifest2 := buildOIDCAuthProviderYAML(apName2, issuerURL, clientID2, secret2)
				_, err = applyManifest(harness, manifest2)
				Expect(err).ToNot(HaveOccurred(), "create second AuthProvider")

				DeferCleanup(func() {
					_ = deleteAuthProvider(harness, apName1)
					_ = deleteAuthProvider(harness, apName2)
				})

				By("creating an SSH Repository with a known private key")
				Expect(auxSvcs.GitServer).ToNot(BeNil(), "git server must be started")
				repoName := "enc-neg-repo-" + testID
				keyContent, err := auxSvcs.GetGitSSHPrivateKey()
				Expect(err).ToNot(HaveOccurred(), "get git SSH key")
				repoURL := fmt.Sprintf("user@%s:%d:/home/user/repos/test.git",
					auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)
				Expect(harness.CreateRepositoryWithSSHCredentials(repoName, repoURL, keyContent)).To(
					Succeed(), "create SSH Repository")

				DeferCleanup(func() {
					_, _ = harness.ManageResource("delete", "repository", repoName)
				})

				By("[N3] listing AuthProviders and verifying clientSecret is redacted")
				apListResp, err := harness.Client.ListAuthProvidersWithResponse(ctx, nil)
				Expect(err).ToNot(HaveOccurred(), "LIST AuthProviders")
				Expect(apListResp.StatusCode()).To(Equal(200), "LIST AuthProviders must return 200")
				apListBody := string(apListResp.Body)
				Expect(apListBody).ToNot(ContainSubstring(secret1),
					"LIST AuthProviders must not expose first plaintext clientSecret")
				Expect(apListBody).ToNot(ContainSubstring(secret2),
					"LIST AuthProviders must not expose second plaintext clientSecret")
				Expect(apListBody).To(ContainSubstring("*****"),
					"LIST AuthProviders must show redacted placeholder")

				By("[N3] listing Repositories and verifying sshPrivateKey is redacted")
				repoListResp, err := harness.Client.ListRepositoriesWithResponse(ctx, nil)
				Expect(err).ToNot(HaveOccurred(), "LIST Repositories")
				Expect(repoListResp.StatusCode()).To(Equal(200), "LIST Repositories must return 200")
				repoListBody := string(repoListResp.Body)
				Expect(repoListBody).ToNot(ContainSubstring("BEGIN"),
					"LIST Repositories must not contain PEM headers")
				Expect(repoListBody).To(ContainSubstring("*****"),
					"LIST Repositories must show redacted placeholder")
			})
	})

	// N1: A plaintext value stored directly in DB (e.g. pre-migration data) is
	// re-encrypted on the next API write. The plaintext-passthrough path in
	// Manager.Decrypt is backward-compatible; ProcessEncryption re-encrypts on save.
	Context("When a sensitive field stored as plaintext in the DB is updated via the API", func() {
		It("N1: should store the field encrypted after the update", Label("90113"), func() {
			skipIfNoBuiltinDB()
			ctx := harness.GetTestContext()
			testID := harness.GetTestIDFromContext()
			Expect(auxSvcs.Keycloak).ToNot(BeNil(), "Keycloak must be started")

			authProviderName := "enc-neg-n1-" + testID
			clientID := "enc-neg-n1-c-" + testID
			issuerURL := auxSvcs.Keycloak.IssuerURL()
			initialSecret := "initial-secret-" + testID

			By("creating an AuthProvider via the API (gets a valid ciphertext in DB)")
			manifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, clientID, initialSecret)
			_, err := applyManifest(harness, manifest)
			Expect(err).ToNot(HaveOccurred(), "create AuthProvider")

			DeferCleanup(func() {
				_ = deleteAuthProvider(harness, authProviderName)
			})

			By("verifying initial DB value is encrypted")
			dbVal, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(dbVal, defaultKeyID)).To(BeTrue(),
				"initial DB value must be encrypted, got: %s", dbVal)

			By("overwriting the DB column with a plaintext value directly")
			plaintextValue := "plaintext-injected-" + testID
			_, err = queryDB(providers, fmt.Sprintf(
				`UPDATE auth_providers SET spec = jsonb_set(spec, '{clientSecret}', '"%s"') WHERE name = '%s'`,
				plaintextValue, authProviderName,
			))
			Expect(err).ToNot(HaveOccurred(), "inject plaintext into DB")

			By("confirming the DB now contains plaintext (no enc: prefix)")
			rawVal, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(rawVal).To(Equal(plaintextValue), "DB should contain injected plaintext")

			By("confirming GET still returns redacted value (not the plaintext)")
			getResp, err := harness.Client.GetAuthProviderWithResponse(ctx, authProviderName)
			Expect(err).ToNot(HaveOccurred(), "GET AuthProvider")
			Expect(getResp.StatusCode()).To(Equal(200))
			Expect(string(getResp.Body)).ToNot(ContainSubstring(plaintextValue),
				"GET must not expose the injected plaintext value")
			Expect(string(getResp.Body)).To(ContainSubstring("*****"),
				"GET must show redacted placeholder")

			By("[N1] updating the AuthProvider via the API to trigger a write")
			newSecret := "re-encrypted-secret-" + testID
			updatedManifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, clientID, newSecret)
			_, err = applyManifest(harness, updatedManifest)
			Expect(err).ToNot(HaveOccurred(), "update AuthProvider to trigger re-encryption")

			By("[N1] verifying the DB value is now encrypted under the active key")
			dbValAfter, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred())
			Expect(ciphertextMatchesKeyID(dbValAfter, defaultKeyID)).To(BeTrue(),
				"[N1] DB value must be encrypted after write; got: %s", dbValAfter)
			Expect(dbValAfter).ToNot(ContainSubstring(newSecret),
				"[N1] DB must not store the new secret in plaintext")
		})
	})

	// N4: A client submitting a pre-formed enc:v1: prefixed string as a credential
	// value must not succeed. The GORM plugin tries to decrypt it (ProcessEncryption
	// treats the enc: prefix as an encrypted value), fails GCM authentication, and
	// aborts the DB write. The resource must not be created.
	Context("When a client submits a pre-formed enc:v1: value as a credential", func() {
		It("N4: should reject the request — not store the fake ciphertext", Label("90110"), func() {
			skipIfNoBuiltinDB()
			testID := harness.GetTestIDFromContext()
			Expect(auxSvcs.Keycloak).ToNot(BeNil(), "Keycloak must be started")

			authProviderName := "enc-neg-n4-" + testID
			clientID := "enc-neg-n4-c-" + testID
			issuerURL := auxSvcs.Keycloak.IssuerURL()

			// Syntactically valid enc:v1: prefix with base64-decodable payload that
			// is long enough to contain a nonce (≥28 bytes decoded) but whose GCM
			// authentication tag will not verify against the server's real AES key.
			fakeSecret := "enc:v1:default:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="

			DeferCleanup(func() {
				// Best-effort: resource should not exist, but clean up in case of partial success.
				_ = deleteAuthProvider(harness, authProviderName)
			})

			By("[N4] attempting to create an AuthProvider with a fake enc:v1: clientSecret")
			manifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, clientID, fakeSecret)
			_, err := applyManifest(harness, manifest)
			Expect(err).To(HaveOccurred(),
				"[N4] apply with fake enc:v1: secret must return an error")

			By("[N4] verifying no AuthProvider was persisted in the DB")
			dbVal, err := queryDB(providers, fmt.Sprintf(
				"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
			))
			Expect(err).ToNot(HaveOccurred(), "DB query must not error (resource absence is not a query error)")
			Expect(dbVal).To(BeEmpty(),
				"[N4] no AuthProvider must exist in DB after rejected write; got: %s", dbVal)
		})
	})

	// N2: A Repository whose encrypted credential is corrupted directly in the DB
	// transitions to Accessible=False when the background task attempts decryption.
	// Restoring the original ciphertext causes recovery to Accessible=True.
	// The service must not crash — API must remain reachable throughout.
	Context("When a Repository's encrypted credential is corrupted in the DB", func() {
		It("N2: should mark the Repository Accessible=False and recover when the value is restored",
			Label("90112"), func() {
				skipIfNoBuiltinDB()
				testID := harness.GetTestIDFromContext()
				Expect(auxSvcs.GitServer).ToNot(BeNil(), "git server must be started")

				// Use a unique repo name derived from the test ID so concurrent test runs don't collide.
				gitRepoName := "n2-" + testID
				repoName := "enc-neg-n2-" + testID

				keyPath, err := auxSvcs.GetGitSSHPrivateKeyPath()
				Expect(err).ToNot(HaveOccurred(), "get git SSH key path")
				keyContent, err := auxSvcs.GetGitSSHPrivateKey()
				Expect(err).ToNot(HaveOccurred(), "get git SSH key content")

				gitServerConfig := e2e.GitServerConfig{
					Host: auxSvcs.GitServer.Host,
					Port: auxSvcs.GitServer.Port,
					User: "user",
				}

				By("creating a bare git repo on the server so the repository can become Accessible=True")
				Expect(harness.CreateGitRepositoryOnServer(gitServerConfig, keyPath, gitRepoName)).To(
					Succeed(), "create bare git repo on server")
				// Register git-server cleanup immediately so leaks are prevented even if subsequent
				// steps (CreateRepositoryWithSSHCredentials) fail before the shared DeferCleanup below.
				DeferCleanup(func() {
					if err := harness.DeleteGitRepositoryOnServer(gitServerConfig, keyPath, gitRepoName); err != nil {
						GinkgoWriter.Printf("Warning: failed to delete git server repo %s: %v\n", gitRepoName, err)
					}
				})

				// Use InternalHost/InternalPort for the Repository URL so that repotester
				// (running inside the cluster) can reach the git server via the correct
				// network path. On OCP, E2E_AUX_HOST must be set to a cluster-reachable
				// address for the git server to be accessible.
				repoURL, err := harness.GetInternalGitRepoURL(
					auxSvcs.GitServer.InternalHost, auxSvcs.GitServer.InternalPort, gitRepoName)
				Expect(err).ToNot(HaveOccurred(), "build internal git repo URL")

				// savedCipherN2 captures the original DB ciphertext so DeferCleanup can
				// restore it before deletion (avoids broken-decrypt errors in the cleanup path).
				var savedCipherN2 string

				By("creating an SSH Repository resource pointing to the bare git repo")
				Expect(harness.CreateRepositoryWithSSHCredentials(repoName, repoURL, keyContent)).To(
					Succeed(), "create SSH Repository")

				DeferCleanup(func() {
					// Restore the valid ciphertext before deleting so the delete does not fail
					// due to a broken decrypt during any cleanup read path.
					if savedCipherN2 != "" {
						_, _ = queryDB(providers, fmt.Sprintf(
							`UPDATE repositories SET spec = jsonb_set(spec, '{sshConfig,sshPrivateKey}', '"%s"') WHERE name = '%s'`,
							savedCipherN2, repoName,
						))
					}
					_, _ = harness.ManageResource("delete", "repository", repoName)
				})

				By("waiting for the Repository to become Accessible=True initially")
				Expect(harness.WaitForRepositoryAccessible(repoName,
					testutil.DURATION_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
					Succeed(), "Repository must be accessible before corruption")

				By("saving the valid ciphertext from DB before corruption")
				savedCipherN2, err = queryDB(providers, fmt.Sprintf(
					"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
				))
				Expect(err).ToNot(HaveOccurred())
				Expect(savedCipherN2).To(HavePrefix("enc:v1:"), "saved value must be an encrypted ciphertext")

				// Structurally valid enc:v1: prefix; base64 payload decodes to >32 bytes
				// (satisfies nonce length check) but the GCM authentication tag will not
				// verify, causing DecryptParsed to return ErrDecryptionFailed.
				brokenCipher := "enc:v1:default:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="

				By("corrupting the DB ciphertext directly")
				_, err = queryDB(providers, fmt.Sprintf(
					`UPDATE repositories SET spec = jsonb_set(spec, '{sshConfig,sshPrivateKey}', '"%s"') WHERE name = '%s'`,
					brokenCipher, repoName,
				))
				Expect(err).ToNot(HaveOccurred(), "corrupt ciphertext in DB")

				By("[N2] waiting for the background task to detect decryption failure → Accessible=False")
				Expect(harness.WaitForRepositoryNotAccessible(repoName,
					testutil.LONG_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
					Succeed(), "[N2] Repository must become Accessible=False after ciphertext corruption")

				By("[N2] verifying the API is still reachable (service did not crash)")
				ctx := harness.GetTestContext()
				resp, err := harness.Client.GetRepositoryWithResponse(ctx, repoName)
				Expect(err).ToNot(HaveOccurred(), "[N2] GET Repository must succeed after corruption")
				Expect(resp.StatusCode()).To(Equal(200), "[N2] API must return 200 — service must not crash")

				By("[N2] restoring the original valid ciphertext in DB")
				_, err = queryDB(providers, fmt.Sprintf(
					`UPDATE repositories SET spec = jsonb_set(spec, '{sshConfig,sshPrivateKey}', '"%s"') WHERE name = '%s'`,
					savedCipherN2, repoName,
				))
				Expect(err).ToNot(HaveOccurred(), "restore valid ciphertext")

				By("[N2] verifying the Repository recovers to Accessible=True")
				Expect(harness.WaitForRepositoryAccessible(repoName,
					testutil.LONG_TIMEOUT, testutil.EVENTUALLY_POLLING_250)).To(
					Succeed(), "[N2] Repository must return to Accessible=True after ciphertext restore")
			})
	})
})
