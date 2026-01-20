package imagebuilder_worker_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/tasks"
	"github.com/flightctl/flightctl/internal/service"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/testutil"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"sigs.k8s.io/yaml"
)

var (
	suiteCtx context.Context
)

func TestContainerfileGeneration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Containerfile Generation Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutilpkg.InitSuiteTracerForGinkgo("Containerfile Generation Suite")
})

func newTestImageBuild(name string, bindingType string) api.ImageBuild {
	imageBuild := api.ImageBuild{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       string(api.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageBuildSpec{
			Source: api.ImageBuildSource{
				Repository: "test-repo",
				ImageName:  "test-image",
				ImageTag:   "v1.0.0",
			},
			Destination: api.ImageBuildDestination{
				Repository: "output-repo",
				ImageName:  "output-image",
				Tag:        "v1.0.0",
			},
		},
	}

	if bindingType == "early" {
		binding := api.ImageBuildBinding{}
		_ = binding.FromEarlyBinding(api.EarlyBinding{
			Type: api.Early,
		})
		imageBuild.Spec.Binding = binding
	} else {
		binding := api.ImageBuildBinding{}
		_ = binding.FromLateBinding(api.LateBinding{
			Type: api.Late,
		})
		imageBuild.Spec.Binding = binding
	}

	return imageBuild
}

func createOCIRepository(ctx context.Context, repoStore flightctlstore.Repository, orgId uuid.UUID, name string, registry string, scheme *v1beta1.OciRepoSpecScheme) (*v1beta1.Repository, error) {
	ociSpec := v1beta1.OciRepoSpec{
		Registry: registry,
		Type:     v1beta1.RepoSpecTypeOci,
		Scheme:   scheme,
	}
	spec := v1beta1.RepositorySpec{}
	err := spec.FromOciRepoSpec(ociSpec)
	if err != nil {
		return nil, err
	}
	resource := v1beta1.Repository{
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}

	callback := flightctlstore.EventCallback(func(context.Context, v1beta1.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
	})
	return repoStore.Create(ctx, orgId, &resource, callback)
}

var _ = Describe("Containerfile Generation", func() {
	var (
		log            *logrus.Logger
		ctx            context.Context
		orgId          uuid.UUID
		storeInst      store.Store
		mainStoreInst  flightctlstore.Store
		cfg            *config.Config
		dbName         string
		db             *gorm.DB
		serviceHandler *service.ServiceHandler
	)

	BeforeEach(func() {
		ctx = testutilpkg.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()

		// Use main store's PrepareDBForUnitTests which includes organizations table
		mainStoreInst, cfg, dbName, db = flightctlstore.PrepareDBForUnitTests(ctx, log)

		// Create imagebuilder store on the same db connection
		storeInst = store.NewStore(db, log.WithField("pkg", "imagebuilder-store"))

		// Run imagebuilder-specific migrations only for local strategy
		strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
		if strategy != testutil.StrategyTemplate {
			err := storeInst.RunMigrations(ctx)
			Expect(err).ToNot(HaveOccurred())
		}

		// Setup CA for enrollment credential generation (needed for early binding)
		testDirPath := GinkgoT().TempDir()
		caCfg := ca.NewDefault(testDirPath)
		caClient, _, err := icrypto.EnsureCA(caCfg)
		Expect(err).ToNot(HaveOccurred())

		// Create test organization (required for foreign key constraint)
		orgId = uuid.New()
		err = testutilpkg.CreateTestOrganization(ctx, mainStoreInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		// Create service handler for enrollment credential generation
		serviceHandler = service.NewServiceHandler(mainStoreInst, nil, nil, caClient, log, "https://api.example.com", "https://ui.example.com", []string{})
	})

	AfterEach(func() {
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStoreInst, dbName)
	})

	Context("Late binding containerfile generation", func() {
		It("should generate containerfile with correct content for late binding", func() {
			// Create OCI repository
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "quay.io", nil)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild with late binding
			imageBuild := newTestImageBuild("test-build", "late")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Load the ImageBuild from store
			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			// Generate containerfile
			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Containerfile).ToNot(BeEmpty())
			Expect(result.AgentConfig).To(BeNil(), "Late binding should not have agent config")

			// Verify Containerfile content
			containerfile := result.Containerfile
			Expect(containerfile).To(ContainSubstring("FROM quay.io/test-image:v1.0.0"))
			Expect(containerfile).To(ContainSubstring("flightctl-agent"))
			Expect(containerfile).To(ContainSubstring("systemctl enable flightctl-agent.service"))
			Expect(containerfile).To(ContainSubstring("ignition"))
			Expect(containerfile).To(ContainSubstring("cloud-init"))
			Expect(containerfile).ToNot(ContainSubstring("FLIGHTCTL_CONFIG"), "Late binding should not include agent config")
		})
	})

	Context("Early binding containerfile generation", func() {
		It("should generate containerfile with agent config for early binding", func() {
			// Create OCI repository
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "registry.example.com", lo.ToPtr(v1beta1.OciRepoSpecSchemeHttps))
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild with early binding
			imageBuild := newTestImageBuild("test-build-early", "early")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Load the ImageBuild from store
			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build-early")
			Expect(err).ToNot(HaveOccurred())

			// Generate containerfile
			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Containerfile).ToNot(BeEmpty())
			Expect(result.AgentConfig).ToNot(BeNil(), "Early binding should have agent config")
			Expect(result.AgentConfig).ToNot(BeEmpty())

			// Verify Containerfile content
			containerfile := result.Containerfile
			Expect(containerfile).To(ContainSubstring("FROM registry.example.com/test-image:v1.0.0"))
			Expect(containerfile).To(ContainSubstring("flightctl-agent"))
			Expect(containerfile).To(ContainSubstring("systemctl enable flightctl-agent.service"))
			Expect(containerfile).To(ContainSubstring("/etc/flightctl/config.yaml"))
			Expect(containerfile).To(ContainSubstring("chmod 600"))
			Expect(containerfile).To(ContainSubstring("FLIGHTCTL_CONFIG"), "Early binding should include agent config")
			Expect(containerfile).ToNot(ContainSubstring("ignition"), "Early binding should not include ignition")
			Expect(containerfile).ToNot(ContainSubstring("cloud-init"), "Early binding should not include cloud-init")

			// Verify agent config content
			agentConfig := string(result.AgentConfig)
			Expect(agentConfig).To(ContainSubstring("enrollment-service:"))
			Expect(agentConfig).To(ContainSubstring("client-certificate-data:"))
			Expect(agentConfig).To(ContainSubstring("client-key-data:"))
			Expect(agentConfig).To(ContainSubstring("certificate-authority-data:"))

			// Parse and validate agent config YAML structure
			var configMap map[string]interface{}
			err = yaml.Unmarshal(result.AgentConfig, &configMap)
			Expect(err).ToNot(HaveOccurred(), "Agent config should be valid YAML")

			// Validate enrollment-service structure
			enrollmentService, ok := configMap["enrollment-service"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "enrollment-service should be a map")

			// Validate service.server field exists and is a valid URL
			serviceMap, ok := enrollmentService["service"].(map[string]interface{})
			Expect(ok).To(BeTrue(), "service should be a map")
			serverURL, ok := serviceMap["server"].(string)
			Expect(ok).To(BeTrue(), "server should be a string")
			Expect(serverURL).ToNot(BeEmpty(), "server URL should not be empty")
			Expect(serverURL).ToNot(ContainSubstring("https://:"), "server URL should not be malformed (https://:port)")

			// Validate server URL is a valid URL
			parsedURL, err := url.Parse(serverURL)
			Expect(err).ToNot(HaveOccurred(), "server URL should be a valid URL")
			Expect(parsedURL.Scheme).To(Equal("https"), "server URL should use https scheme")
			Expect(parsedURL.Host).ToNot(BeEmpty(), "server URL should have a host")

			// Validate enrollment-ui-endpoint
			uiEndpoint, ok := enrollmentService["enrollment-ui-endpoint"].(string)
			Expect(ok).To(BeTrue(), "enrollment-ui-endpoint should be a string")
			Expect(uiEndpoint).ToNot(BeEmpty(), "enrollment-ui-endpoint should not be empty")

		})
	})

	Context("Repository URL handling", func() {
		It("should handle repository without scheme", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "no-scheme-repo", "quay.io", nil)
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "no-scheme-repo"
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result.Containerfile).To(ContainSubstring("FROM quay.io/test-image:v1.0.0"))
		})

		It("should handle repository with https scheme", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "https-repo", "registry.example.com", lo.ToPtr(v1beta1.OciRepoSpecSchemeHttps))
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "https-repo"
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result.Containerfile).To(ContainSubstring("FROM registry.example.com/test-image:v1.0.0"))
		})

		It("should handle repository with http scheme", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "http-repo", "localhost:5000", lo.ToPtr(v1beta1.OciRepoSpecSchemeHttp))
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "http-repo"
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result.Containerfile).To(ContainSubstring("FROM localhost:5000/test-image:v1.0.0"))
		})
	})

	Context("Error handling", func() {
		It("should return error when repository not found", func() {
			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "nonexistent-repo"
			_, err := storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			_, err = tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("repository"))
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("should return error when repository is not OCI type", func() {
			// Create a Git repository instead of OCI
			gitSpec := v1beta1.RepositorySpec{}
			err := gitSpec.FromGenericRepoSpec(v1beta1.GenericRepoSpec{
				Type: v1beta1.RepoSpecTypeGit,
				Url:  "https://github.com/example/repo.git",
			})
			Expect(err).ToNot(HaveOccurred())
			gitRepo := v1beta1.Repository{
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr("git-repo"),
				},
				Spec: gitSpec,
			}
			callback := flightctlstore.EventCallback(func(context.Context, v1beta1.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
			})
			_, err = mainStoreInst.Repository().Create(ctx, orgId, &gitRepo, callback)
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "git-repo"
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			_, err = tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must be of type 'oci'"))
		})
	})

	Context("Containerfile template validation", func() {
		It("should generate valid Containerfile syntax", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "quay.io", nil)
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			containerfile := result.Containerfile

			// Basic syntax checks
			Expect(containerfile).To(ContainSubstring("FROM"))
			Expect(containerfile).To(ContainSubstring("RUN"))
			Expect(strings.Count(containerfile, "FROM")).To(Equal(1), "Should have exactly one FROM statement")

			// Verify all RUN commands are properly formatted
			lines := strings.Split(containerfile, "\n")
			inRunCommand := false
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "RUN ") {
					inRunCommand = true
					// RUN command should not be empty
					Expect(len(trimmed)).To(BeNumerically(">", 4))
				} else if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					// Empty lines and comments are fine
					continue
				} else if inRunCommand && strings.HasPrefix(trimmed, "RUN ") {
					// New RUN command
					inRunCommand = true
				} else if inRunCommand && !strings.HasPrefix(trimmed, "\\") && trimmed != "" {
					// Continuation line
					inRunCommand = false
				}
			}
		})

		It("should generate unique heredoc delimiters for each build", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "quay.io", nil)
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "early")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			// Generate multiple containerfiles
			delimiters := make(map[string]bool)
			for i := 0; i < 5; i++ {
				result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)
				Expect(err).ToNot(HaveOccurred())

				// Extract heredoc delimiter
				lines := strings.Split(result.Containerfile, "\n")
				for _, line := range lines {
					if strings.Contains(line, "FLIGHTCTL_CONFIG_") {
						parts := strings.Fields(line)
						for _, part := range parts {
							if strings.HasPrefix(part, "FLIGHTCTL_CONFIG_") {
								delimiter := strings.Trim(part, "'")
								Expect(delimiters[delimiter]).To(BeFalse(), "Heredoc delimiter should be unique: %s", delimiter)
								delimiters[delimiter] = true
								break
							}
						}
					}
				}
			}
		})
	})
})
