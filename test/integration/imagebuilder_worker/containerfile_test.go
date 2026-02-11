package imagebuilder_worker_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
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

func TestImageBuilderWorker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ImageBuilder Worker Integration Suite")
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
				ImageTag:   "v1.0.0",
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
		Type:     v1beta1.OciRepoSpecTypeOci,
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

			// Verify BuildArgs are set correctly
			Expect(result.BuildArgs.RegistryHostname).To(Equal("quay.io"))
			Expect(result.BuildArgs.ImageName).To(Equal("test-image"))
			Expect(result.BuildArgs.ImageTag).To(Equal("v1.0.0"))
			Expect(result.BuildArgs.EarlyBinding).To(BeFalse())

			// Verify Containerfile is static template with ARG declarations
			containerfile := result.Containerfile
			Expect(containerfile).To(ContainSubstring("ARG REGISTRY_HOSTNAME"))
			Expect(containerfile).To(ContainSubstring("FROM ${REGISTRY_HOSTNAME}/${IMAGE_NAME}:${IMAGE_TAG}"))
			Expect(containerfile).To(ContainSubstring("flightctl-agent"))
			Expect(containerfile).To(ContainSubstring("systemctl enable flightctl-agent.service"))
			Expect(containerfile).To(ContainSubstring("ignition"))   // In shell conditional
			Expect(containerfile).To(ContainSubstring("cloud-init")) // In shell conditional
		})
	})

	Context("Early binding containerfile generation", func() {
		It("should generate containerfile with agent config for early binding", func() {
			// Create OCI repository
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
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

			// Verify BuildArgs are set correctly
			Expect(result.BuildArgs.RegistryHostname).To(Equal("registry.example.com"))
			Expect(result.BuildArgs.ImageName).To(Equal("test-image"))
			Expect(result.BuildArgs.ImageTag).To(Equal("v1.0.0"))
			Expect(result.BuildArgs.EarlyBinding).To(BeTrue())
			Expect(result.BuildArgs.AgentConfigDestPath).To(Equal("/etc/flightctl/config.yaml"))

			// Verify Containerfile is static template with ARG declarations
			containerfile := result.Containerfile
			Expect(containerfile).To(ContainSubstring("ARG EARLY_BINDING"))
			Expect(containerfile).To(ContainSubstring("ARG AGENT_CONFIG_DEST_PATH"))
			Expect(containerfile).To(ContainSubstring("flightctl-agent"))
			Expect(containerfile).To(ContainSubstring("systemctl enable flightctl-agent.service"))
			Expect(containerfile).To(ContainSubstring("chmod 600"))

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
			// Values are now in BuildArgs, not templated into Containerfile
			Expect(result.BuildArgs.RegistryHostname).To(Equal("quay.io"))
			Expect(result.BuildArgs.ImageName).To(Equal("test-image"))
			Expect(result.BuildArgs.ImageTag).To(Equal("v1.0.0"))
		})

		It("should handle repository with https scheme", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "https-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "https-repo"
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result.BuildArgs.RegistryHostname).To(Equal("registry.example.com"))
		})

		It("should handle repository with http scheme", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "http-repo", "localhost:5000", lo.ToPtr(v1beta1.Http))
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "late")
			imageBuild.Spec.Source.Repository = "http-repo"
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result.BuildArgs.RegistryHostname).To(Equal("localhost:5000"))
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
			err := gitSpec.FromGitRepoSpec(v1beta1.GitRepoSpec{
				Type: v1beta1.GitRepoSpecTypeGit,
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

			// Basic syntax checks - template is now static with ARG declarations
			Expect(containerfile).To(ContainSubstring("ARG REGISTRY_HOSTNAME"))
			Expect(containerfile).To(ContainSubstring("ARG IMAGE_NAME"))
			Expect(containerfile).To(ContainSubstring("ARG IMAGE_TAG"))
			Expect(containerfile).To(ContainSubstring("FROM ${REGISTRY_HOSTNAME}/${IMAGE_NAME}:${IMAGE_TAG}"))
			Expect(containerfile).To(ContainSubstring("RUN"))
			Expect(strings.Count(containerfile, "FROM ${REGISTRY_HOSTNAME}")).To(Equal(1), "Should have exactly one FROM statement")

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

		It("should return consistent static template for multiple generations", func() {
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "quay.io", nil)
			Expect(err).ToNot(HaveOccurred())

			imageBuild := newTestImageBuild("test-build", "early")
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build")
			Expect(err).ToNot(HaveOccurred())

			// Generate multiple containerfiles - they should all be identical (static template)
			var firstContainerfile string
			for i := 0; i < 5; i++ {
				result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)
				Expect(err).ToNot(HaveOccurred())

				if i == 0 {
					firstContainerfile = result.Containerfile
				} else {
					Expect(result.Containerfile).To(Equal(firstContainerfile), "Template should be static and consistent")
				}
			}
		})
	})

	Context("User configuration in containerfile generation", func() {
		It("should generate containerfile with user configuration for late binding", func() {
			// Create OCI repositories (source and destination)
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())
			_, err = createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "output-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild with late binding and user configuration
			testPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQAB test@example.com"
			imageBuild := newTestImageBuild("test-build-userconfig", "late")
			imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
				Username:  "testuser",
				Publickey: testPublicKey,
			}
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Load the ImageBuild from store
			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build-userconfig")
			Expect(err).ToNot(HaveOccurred())

			// Generate containerfile
			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Containerfile).ToNot(BeEmpty())

			// Verify BuildArgs are set correctly for user configuration
			Expect(result.BuildArgs.HasUserConfig).To(BeTrue())
			Expect(result.BuildArgs.Username).To(Equal("testuser"))

			// Verify Publickey is stored for writing to build context
			Expect(result.Publickey).To(Equal([]byte(testPublicKey)))

			// Verify Containerfile has user configuration support (shell conditionals using ARGs)
			containerfile := result.Containerfile
			Expect(containerfile).To(ContainSubstring("ARG HAS_USER_CONFIG"))
			Expect(containerfile).To(ContainSubstring("ARG USERNAME"))
			Expect(containerfile).To(ContainSubstring(`if [ "${HAS_USER_CONFIG}" = "true" ]`))
		})

		It("should generate containerfile with user configuration for early binding", func() {
			// Create OCI repositories (source and destination)
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())
			_, err = createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "output-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild with early binding and user configuration
			testPublicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI test@example.com"
			imageBuild := newTestImageBuild("test-build-userconfig-early", "early")
			imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
				Username:  "admin",
				Publickey: testPublicKey,
			}
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Load the ImageBuild from store
			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build-userconfig-early")
			Expect(err).ToNot(HaveOccurred())

			// Generate containerfile
			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Containerfile).ToNot(BeEmpty())

			// Verify BuildArgs are set correctly for both early binding and user configuration
			Expect(result.BuildArgs.EarlyBinding).To(BeTrue())
			Expect(result.BuildArgs.HasUserConfig).To(BeTrue())
			Expect(result.BuildArgs.Username).To(Equal("admin"))

			// Verify both AgentConfig and Publickey are stored
			Expect(result.AgentConfig).ToNot(BeNil())
			Expect(result.Publickey).To(Equal([]byte(testPublicKey)))
		})

		It("should not include user configuration when not provided", func() {
			// Create OCI repositories (source and destination)
			_, err := createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "test-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())
			_, err = createOCIRepository(ctx, mainStoreInst.Repository(), orgId, "output-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild without user configuration
			imageBuild := newTestImageBuild("test-build-no-user", "late")
			// No UserConfiguration set
			_, err = storeInst.ImageBuild().Create(ctx, orgId, &imageBuild)
			Expect(err).ToNot(HaveOccurred())

			// Load the ImageBuild from store
			loadedBuild, err := storeInst.ImageBuild().Get(ctx, orgId, "test-build-no-user")
			Expect(err).ToNot(HaveOccurred())

			// Generate containerfile
			result, err := tasks.GenerateContainerfile(ctx, mainStoreInst, serviceHandler, orgId, loadedBuild, log)

			Expect(err).ToNot(HaveOccurred())
			Expect(result).ToNot(BeNil())
			Expect(result.Containerfile).ToNot(BeEmpty())

			// Verify BuildArgs indicate no user configuration
			Expect(result.BuildArgs.HasUserConfig).To(BeFalse())
			Expect(result.BuildArgs.Username).To(BeEmpty())

			// Verify Publickey is nil when no user configuration
			Expect(result.Publickey).To(BeNil())
		})
	})
})
