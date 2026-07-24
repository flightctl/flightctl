package encryption_test

import (
	"encoding/base64"
	"fmt"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Encryption at rest — AuthProvider", Label("encryption"), func() {
	var (
		harness      *e2e.Harness
		providers    *infra.Providers
		clientSecret string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()
		clientSecret = "e2e-secret-" + harness.GetTestIDFromContext()
		Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")
		Expect(auxSvcs.Keycloak).ToNot(BeNil(), "Keycloak must be started")
	})

	Context("When an OIDC AuthProvider with a clientSecret is created", func() {
		It("should redact clientSecret in GET response and store it encrypted in the database",
			Label("sanity"), func() {

				ctx := harness.GetTestContext()
				authProviderName := "enc-ap-" + harness.GetTestIDFromContext()

				By("creating an OIDC AuthProvider with a known clientSecret")
				issuerURL := auxSvcs.Keycloak.IssuerURL()
				manifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, "flightctl-client", clientSecret)
				out, err := applyManifest(harness, manifest)
				Expect(err).ToNot(HaveOccurred(), "apply AuthProvider manifest")
				Expect(out).ToNot(BeEmpty(), "apply must produce output")

				DeferCleanup(func() {
					if deleteErr := deleteAuthProvider(harness, authProviderName); deleteErr != nil {
						GinkgoWriter.Printf("Warning: failed to delete authprovider %s: %v\n", authProviderName, deleteErr)
					}
				})

				By("fetching the AuthProvider via the API")
				resp, err := harness.Client.GetAuthProviderWithResponse(ctx, authProviderName)
				Expect(err).ToNot(HaveOccurred(), "GET AuthProvider")
				Expect(resp.StatusCode()).To(Equal(200), "GET AuthProvider must return 200")
				Expect(resp.JSON200).ToNot(BeNil(), "GET response body must parse")

				By("verifying clientSecret is redacted in the API response (§2.6)")
				rawBody := string(resp.Body)
				Expect(rawBody).ToNot(ContainSubstring(clientSecret),
					"API response body must not contain the plaintext clientSecret")
				Expect(rawBody).To(ContainSubstring("*****"),
					"API response body must show redacted placeholder")

				By("querying the database and verifying the ciphertext format (§2.7)")
				dbVal, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
				))
				Expect(err).ToNot(HaveOccurred(), "psql query for clientSecret")
				Expect(dbVal).ToNot(BeEmpty(), "DB value must not be empty")
				Expect(dbVal).ToNot(ContainSubstring(clientSecret),
					"DB must not store the plaintext clientSecret")
				Expect(ciphertextMatchesKeyID(dbVal, defaultKeyID)).To(BeTrue(),
					"DB value must start with enc:v1:%s: but got: %s", defaultKeyID, dbVal)

				firstCiphertext := dbVal

				By("updating the AuthProvider with a new clientSecret (§4.2)")
				newSecret := "e2e-newsecret-" + harness.GetTestIDFromContext()
				updatedManifest := buildOIDCAuthProviderYAML(authProviderName, issuerURL, "flightctl-client", newSecret)
				out, err = applyManifest(harness, updatedManifest)
				Expect(err).ToNot(HaveOccurred(), "update AuthProvider with new clientSecret")
				Expect(out).ToNot(BeEmpty(), "update must produce output")

				By("verifying the updated API response still redacts the secret")
				resp2, err := harness.Client.GetAuthProviderWithResponse(ctx, authProviderName)
				Expect(err).ToNot(HaveOccurred(), "GET updated AuthProvider")
				Expect(resp2.StatusCode()).To(Equal(200), "GET updated AuthProvider must return 200")
				Expect(string(resp2.Body)).ToNot(ContainSubstring(newSecret),
					"updated API response must not expose the new plaintext secret")

				By("verifying the DB value changed and is still encrypted (§4.2)")
				dbVal2, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->>'clientSecret' FROM auth_providers WHERE name = '%s'", authProviderName,
				))
				Expect(err).ToNot(HaveOccurred(), "psql query after update")
				Expect(dbVal2).ToNot(BeEmpty(), "updated DB value must not be empty")
				Expect(ciphertextMatchesKeyID(dbVal2, defaultKeyID)).To(BeTrue(),
					"updated DB value must start with enc:v1:%s: but got: %s", defaultKeyID, dbVal2)
				Expect(dbVal2).ToNot(Equal(firstCiphertext),
					"ciphertext must change after credential update (re-encrypted on save)")
				Expect(dbVal2).ToNot(ContainSubstring(newSecret),
					"DB must not store the new plaintext secret")
			})
	})
})

var _ = Describe("Encryption at rest — Repository", Label("encryption"), func() {
	var (
		harness   *e2e.Harness
		providers *infra.Providers
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
		providers = setup.GetDefaultProviders()

		Expect(auxSvcs).ToNot(BeNil(), "auxiliary services must be initialized")
		Expect(auxSvcs.GitServer).ToNot(BeNil(), "git server must be started")
	})

	Context("When a Repository with SSH credentials is created", func() {
		It("should redact sshPrivateKey in GET response and store it encrypted in the database",
			Label("sanity"), func() {

				repoName := "enc-repo-ssh-" + harness.GetTestIDFromContext()
				repoURL := fmt.Sprintf("user@%s:%d:/home/user/repos/test.git",
					auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)

				By("creating an SSH-auth Repository")
				keyContent, err := auxSvcs.GetGitSSHPrivateKey()
				Expect(err).ToNot(HaveOccurred(), "get git server SSH key")
				err = harness.CreateRepositoryWithSSHCredentials(repoName, repoURL, keyContent)
				Expect(err).ToNot(HaveOccurred(), "create SSH Repository")

				By("fetching the Repository via the API")
				ctx := harness.GetTestContext()
				resp, err := harness.Client.GetRepositoryWithResponse(ctx, repoName)
				Expect(err).ToNot(HaveOccurred(), "GET Repository")
				Expect(resp.StatusCode()).To(Equal(200), "GET Repository must return 200")
				Expect(resp.JSON200).ToNot(BeNil(), "GET response body must parse")

				By("verifying sshPrivateKey is redacted in API response (§3.5)")
				rawBody := string(resp.Body)
				Expect(rawBody).ToNot(ContainSubstring("BEGIN"),
					"API response body must not contain PEM key material")
				Expect(rawBody).To(ContainSubstring("*****"),
					"API response body must show redacted placeholder")
				rawKeyB64 := base64.StdEncoding.EncodeToString([]byte(keyContent))
				Expect(rawBody).ToNot(ContainSubstring(rawKeyB64),
					"API response body must not contain base64-encoded key material")

				By("querying the database and verifying ciphertext format (§3.6)")
				dbVal, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
				))
				Expect(err).ToNot(HaveOccurred(), "psql query for sshPrivateKey")
				Expect(dbVal).ToNot(BeEmpty(), "DB value must not be empty")
				Expect(dbVal).ToNot(ContainSubstring("BEGIN"),
					"DB must not contain PEM headers")
				Expect(ciphertextMatchesKeyID(dbVal, defaultKeyID)).To(BeTrue(),
					"DB value must start with enc:v1:%s: but got: %s", defaultKeyID, dbVal)

				firstCiphertext := dbVal

				By("updating the Repository with a new credential to verify re-encryption (§4.1)")
				// AES-GCM uses a random nonce per encryption, so ciphertext differs even for same plaintext.
				updatedManifest := fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: Repository
metadata:
  name: %s
spec:
  repo:
    url: %s
    type: git
    sshConfig:
      sshPrivateKey: %s
      skipServerVerification: true
`, repoName, repoURL, base64.StdEncoding.EncodeToString([]byte(keyContent)))
				out, err := applyManifest(harness, updatedManifest)
				Expect(err).ToNot(HaveOccurred(), "update Repository via apply")
				Expect(out).ToNot(BeEmpty(), "update must produce output")

				By("verifying the re-encrypted DB value is still in ciphertext format")
				dbVal2, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->'sshConfig'->>'sshPrivateKey' FROM repositories WHERE name = '%s'", repoName,
				))
				Expect(err).ToNot(HaveOccurred(), "psql query after SSH key update")
				Expect(dbVal2).ToNot(BeEmpty(), "updated DB value must not be empty")
				Expect(ciphertextMatchesKeyID(dbVal2, defaultKeyID)).To(BeTrue(),
					"updated DB value must start with enc:v1:%s: but got: %s", defaultKeyID, dbVal2)
				Expect(dbVal2).ToNot(Equal(firstCiphertext),
					"ciphertext must differ after update (new random nonce per encryption)")
			})
	})

	Context("When a Repository with HTTP credentials is created", func() {
		It("should redact password in GET response and store it encrypted in the database",
			Label("sanity"), func() {

				repoName := "enc-repo-http-" + harness.GetTestIDFromContext()
				repoURL := fmt.Sprintf("http://%s:%d/test.git",
					auxSvcs.GitServer.Host, auxSvcs.GitServer.Port)

				username := "testuser"
				password := "e2e-http-pass-" + harness.GetTestIDFromContext()

				By("creating an HTTP-auth Repository")
				_, err := harness.CreateHTTPRepository(repoName, repoURL, &username, &password)
				Expect(err).ToNot(HaveOccurred(), "create HTTP Repository")

				By("fetching the Repository via the API")
				ctx := harness.GetTestContext()
				resp, err := harness.Client.GetRepositoryWithResponse(ctx, repoName)
				Expect(err).ToNot(HaveOccurred(), "GET Repository")
				Expect(resp.StatusCode()).To(Equal(200), "GET Repository must return 200")

				By("verifying password is redacted in API response (§3.5)")
				rawBody := string(resp.Body)
				Expect(rawBody).ToNot(ContainSubstring(password),
					"API response body must not contain the plaintext HTTP password")
				Expect(rawBody).To(ContainSubstring("*****"),
					"API response body must show redacted placeholder")

				By("querying the database and verifying ciphertext format (§3.6)")
				dbVal, err := queryDB(providers, fmt.Sprintf(
					"SELECT spec->'httpConfig'->>'password' FROM repositories WHERE name = '%s'", repoName,
				))
				Expect(err).ToNot(HaveOccurred(), "psql query for HTTP password")
				Expect(dbVal).ToNot(BeEmpty(), "DB value must not be empty")
				Expect(dbVal).ToNot(ContainSubstring(password),
					"DB must not store the plaintext HTTP password")
				Expect(ciphertextMatchesKeyID(dbVal, defaultKeyID)).To(BeTrue(),
					"DB value must start with enc:v1:%s: but got: %s", defaultKeyID, dbVal)
			})
	})
})
