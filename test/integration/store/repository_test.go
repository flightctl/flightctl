package store_test

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// decryptString decrypts an encrypted string field value and asserts it was actually encrypted.
func decryptString(val string) string {
	plaintext, wasEncrypted, err := encryption.Decrypt(context.Background(), encryption.Ciphertext(val))
	Expect(err).ToNot(HaveOccurred())
	Expect(wasEncrypted).To(BeTrue(), "expected value to be encrypted but got plaintext: %s", val)
	return string(plaintext)
}

func newOciAuth(username, password string) *api.OciAuth {
	auth := &api.OciAuth{}
	_ = auth.FromDockerAuth(api.DockerAuth{
		Username: username,
		Password: password,
	})
	return auth
}

var _ = Describe("RepositoryStore create", func() {
	var (
		log                 *logrus.Logger
		ctx                 context.Context
		orgId               uuid.UUID
		repositoryStore     repositorystore.Store
		deviceStore         devicestore.Store
		fleetStore          fleetstore.Store
		organizationStore   organizationstore.Store
		cfg                 *config.Config
		dbName              string
		db                  *gorm.DB
		numRepositories     int
		eventCallbackCalled bool
		eventCallback       store.EventCallback
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		numRepositories = 3
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		repositoryStore = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		organizationStore = organizationstore.NewOrganizationStore(db)
		eventCallbackCalled = false
		eventCallback = store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
			eventCallbackCalled = true
		})

		orgId = uuid.New()
		err = testutil.CreateTestOrganization(ctx, organizationStore, orgId)
		Expect(err).ToNot(HaveOccurred())

		err = testutil.CreateRepositories(ctx, 3, repositoryStore, orgId)
		Expect(err).ToNot(HaveOccurred())

		nilrepo := model.Repository{Resource: model.Resource{OrgID: orgId, Name: "nilspec"}}
		result := db.WithContext(ctx).Create(&nilrepo)
		Expect(result.Error).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("Repository store", func() {
		It("Get repository success", func() {
			repo, err := repositoryStore.Get(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("myrepository-1"))
		})

		It("Get repository - not found error", func() {
			_, err := repositoryStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get repository - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := repositoryStore.Get(ctx, badOrgId, "myrepository-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get repository - nil spec - not found error", func() {
			_, err := repositoryStore.Get(ctx, orgId, "nilspec")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete repository success", func() {
			err := repositoryStore.Delete(ctx, orgId, "myrepository-1", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
		})

		It("Delete repository success when not found", func() {
			err := repositoryStore.Delete(ctx, orgId, "nonexistent", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeFalse())
		})

		It("Delete repository success when nil spec", func() {
			err := repositoryStore.Delete(ctx, orgId, "nilspec", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeFalse())
		})

		It("List with paging", func() {
			// Delete the repo with nilspec so it doesn't interfere with the counts
			nilrepo := model.Repository{Resource: model.Resource{OrgID: orgId, Name: "nilspec"}}
			result := db.WithContext(ctx).Delete(&nilrepo)
			Expect(result.Error).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 1000}
			allRepositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allRepositories.Items)).To(Equal(numRepositories))
			allRepoNames := make([]string, len(allRepositories.Items))
			for i, repo := range allRepositories.Items {
				allRepoNames[i] = *repo.Metadata.Name
			}

			foundRepoNames := make([]string, len(allRepositories.Items))
			listParams.Limit = 1
			repositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundRepoNames[0] = *repositories.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(repositories.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			repositories, err = repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundRepoNames[1] = *repositories.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(repositories.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			repositories, err = repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(repositories.Metadata.RemainingItemCount).To(BeNil())
			Expect(repositories.Metadata.Continue).To(BeNil())
			foundRepoNames[2] = *repositories.Items[0].Metadata.Name

			for i := range allRepoNames {
				Expect(allRepoNames[i]).To(Equal(foundRepoNames[i]))
			}
		})

		It("List with labels", func() {
			listParams := store.ListParams{
				Limit:         1000,
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"key": "value-1"})}
			repositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("myrepository-1"))
		})

		It("CreateOrUpdateRepository create mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "myrepo",
				Type: api.GitRepoSpecTypeGit,
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateRepository update mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "myotherrepo",
				Type: api.GitRepoSpecTypeGit,
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myrepository-1"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(created).To(Equal(false))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateRepository create nilspec", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "myotherrepo",
				Type: api.GitRepoSpecTypeGit,
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("nilspec"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("Delete repo with fleet association", func() {
			testutil.CreateTestFleets(ctx, 1, fleetStore, orgId, "myfleet", false, nil)

			err := fleetStore.OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = repositoryStore.Delete(ctx, orgId, "myrepository-1", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
		})

		It("Delete repo with device association", func() {
			testutil.CreateTestDevices(ctx, 1, deviceStore, orgId, nil, false)

			err := deviceStore.OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := deviceStore.GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = repositoryStore.Delete(ctx, orgId, "myrepository-1", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
		})

		It("CountByOrg - with specific orgId", func() {
			// Test with specific orgId
			results, err := repositoryStore.CountByOrg(ctx, &orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].OrgID).To(Equal(orgId.String()))
			Expect(results[0].Count).To(Equal(int64(4))) // 4 repositories from BeforeEach

			// Create additional repositories in the same org with unique names
			for i := 10; i <= 11; i++ {
				spec := api.RepositorySpec{}
				err := spec.FromGitRepoSpec(api.GitRepoSpec{
					Url:  "myrepo",
					Type: api.GitRepoSpecTypeGit,
				})
				Expect(err).ToNot(HaveOccurred())

				resource := api.Repository{
					Metadata: api.ObjectMeta{
						Name:   lo.ToPtr(fmt.Sprintf("myrepository-%d", i)),
						Labels: &map[string]string{"environment": "test"},
					},
					Spec: spec,
				}
				_, err = repositoryStore.Create(ctx, orgId, &resource, eventCallback)
				Expect(err).ToNot(HaveOccurred())
			}

			results, err = repositoryStore.CountByOrg(ctx, &orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].OrgID).To(Equal(orgId.String()))
			Expect(results[0].Count).To(Equal(int64(6))) // 4 original + 2 new
		})

		It("CountByOrg - with nil orgId (all orgs)", func() {
			// Create repositories in another organization
			otherOrgId := uuid.New()
			err := testutil.CreateTestOrganization(ctx, organizationStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())
			err = testutil.CreateRepositories(ctx, 2, repositoryStore, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			// Test with nil orgId (should get all orgs)
			results, err := repositoryStore.CountByOrg(ctx, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(2)) // Should have results for both organizations

			// Verify both organizations are present
			orgIds := make(map[string]int64)
			for _, result := range results {
				orgIds[result.OrgID] = result.Count
			}
			Expect(orgIds).To(HaveKey(orgId.String()))
			Expect(orgIds).To(HaveKey(otherOrgId.String()))
			Expect(orgIds[orgId.String()]).To(Equal(int64(4)))      // Original org has 4 repositories
			Expect(orgIds[otherOrgId.String()]).To(Equal(int64(2))) // Other org has 2 repositories
		})

		It("Create OCI repository with credentials", func() {
			spec := api.RepositorySpec{}
			accessMode := api.ReadWrite
			err := spec.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "quay.io",
				Type:       "oci",
				AccessMode: &accessMode,
				OciAuth:    newOciAuth("myuser", "mypassword"),
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("oci-repo-rw"),
					Labels: &map[string]string{"type": "oci"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))

			// Verify OCI spec is preserved
			ociSpec, err := repo.Spec.AsOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("quay.io"))
			Expect(ociSpec.Type).To(Equal(api.OciRepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.ReadWrite))
			Expect(ociSpec.OciAuth).ToNot(BeNil())
			dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
			Expect(err).ToNot(HaveOccurred())
			Expect(dockerAuth.Username).To(Equal("myuser"))
			Expect(decryptString(dockerAuth.Password)).To(Equal("mypassword"))
		})

		It("Create OCI repository without credentials (public registry)", func() {
			spec := api.RepositorySpec{}
			accessMode := api.Read
			err := spec.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "registry.redhat.io",
				Type:       "oci",
				AccessMode: &accessMode,
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("oci-repo-public"),
					Labels: &map[string]string{"type": "oci"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify OCI spec without credentials
			ociSpec, err := repo.Spec.AsOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("registry.redhat.io"))
			Expect(*ociSpec.AccessMode).To(Equal(api.Read))
			Expect(ociSpec.OciAuth).To(BeNil())
		})

		It("List OCI repositories by accessMode using FieldSelector", func() {
			// Create OCI repositories with different access modes
			specRw := api.RepositorySpec{}
			accessModeRw := api.ReadWrite
			err := specRw.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "quay.io",
				Type:       "oci",
				AccessMode: &accessModeRw,
			})
			Expect(err).ToNot(HaveOccurred())

			repoRw := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oci-output-registry"),
				},
				Spec: specRw,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repoRw, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			specR := api.RepositorySpec{}
			accessModeR := api.Read
			err = specR.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "registry.redhat.io",
				Type:       "oci",
				AccessMode: &accessModeR,
			})
			Expect(err).ToNot(HaveOccurred())

			repoR := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oci-input-registry"),
				},
				Spec: specR,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repoR, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List only read-write repositories
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.accessMode": "ReadWrite"}),
			}
			repositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-output-registry"))

			// Verify the returned repository has correct OCI spec
			ociSpec, err := repositories.Items[0].Spec.AsOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("quay.io"))
			Expect(ociSpec.Type).To(Equal(api.OciRepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.ReadWrite))

			// List only read-only repositories
			listParams.FieldSelector = selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.accessMode": "Read"})
			repositories, err = repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-input-registry"))

			// Verify the returned repository has correct OCI spec
			ociSpec, err = repositories.Items[0].Spec.AsOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("registry.redhat.io"))
			Expect(ociSpec.Type).To(Equal(api.OciRepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.Read))
		})

		It("Get OCI repository and verify accessMode field", func() {
			// Create an OCI repository with ReadWrite access mode
			spec := api.RepositorySpec{}
			accessMode := api.ReadWrite
			err := spec.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "quay.io",
				Type:       "oci",
				AccessMode: &accessMode,
				OciAuth:    newOciAuth("testuser", "testpass"),
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oci-get-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Get the repository by name
			repo, err := repositoryStore.Get(ctx, orgId, "oci-get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("oci-get-test"))

			// Verify OCI spec fields including accessMode
			ociSpec, err := repo.Spec.AsOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("quay.io"))
			Expect(ociSpec.Type).To(Equal(api.OciRepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.ReadWrite))
			Expect(ociSpec.OciAuth).ToNot(BeNil())
			dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
			Expect(err).ToNot(HaveOccurred())
			Expect(dockerAuth.Username).To(Equal("testuser"))
			Expect(decryptString(dockerAuth.Password)).To(Equal("testpass"))
		})

		It("List OCI repositories with combined type and accessMode FieldSelector", func() {
			// Create an OCI repository with Read access
			specOciRead := api.RepositorySpec{}
			accessModeR := api.Read
			err := specOciRead.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "docker.io",
				Type:       "oci",
				AccessMode: &accessModeR,
			})
			Expect(err).ToNot(HaveOccurred())

			repoOciRead := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oci-combined-read"),
				},
				Spec: specOciRead,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repoOciRead, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Create an OCI repository with ReadWrite access
			specOciRw := api.RepositorySpec{}
			accessModeRw := api.ReadWrite
			err = specOciRw.FromOciRepoSpec(api.OciRepoSpec{
				Registry:   "gcr.io",
				Type:       "oci",
				AccessMode: &accessModeRw,
			})
			Expect(err).ToNot(HaveOccurred())

			repoOciRw := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oci-combined-rw"),
				},
				Spec: specOciRw,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repoOciRw, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List with combined filter: type=oci AND accessMode=ReadWrite
			listParams := store.ListParams{
				Limit: 1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{
					"spec.type":       "oci",
					"spec.accessMode": "ReadWrite",
				}),
			}
			repositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-combined-rw"))

			// List with combined filter: type=oci AND accessMode=Read
			listParams.FieldSelector = selector.NewFieldSelectorFromMapOrDie(map[string]string{
				"spec.type":       "oci",
				"spec.accessMode": "Read",
			})
			repositories, err = repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-combined-read"))
		})

		It("List repositories by type using FieldSelector", func() {
			// Create an OCI repository
			spec := api.RepositorySpec{}
			err := spec.FromOciRepoSpec(api.OciRepoSpec{
				Registry: "quay.io",
				Type:     "oci",
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("oci-type-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List only OCI type repositories
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.type": "oci"}),
			}
			repositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-type-test"))

			// List only git type repositories (existing ones from BeforeEach)
			listParams.FieldSelector = selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.type": "git"})
			repositories, err = repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(numRepositories)) // Original git repos
		})

		// SSH Repository CRUD tests
		It("Create SSH repository with credentials", func() {
			spec := api.RepositorySpec{}
			privateKey := "c3NoLXJzYSBBQUFBQjNOemFDMXljMkVBQUFBREFRQUJBQUFCZ1FDN2..." // base64 encoded
			passphrase := "mysecretpassphrase"
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "git@github.com:flightctl/flightctl.git",
				Type: api.GitRepoSpecTypeGit,
				SshConfig: &api.SshConfig{
					SshPrivateKey:        &privateKey,
					PrivateKeyPassphrase: &passphrase,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("ssh-repo-with-creds"),
					Labels: &map[string]string{"type": "ssh"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))

			// Verify SSH spec is preserved
			gitSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(gitSpec.Url).To(Equal("git@github.com:flightctl/flightctl.git"))
			Expect(gitSpec.Type).To(Equal(api.GitRepoSpecTypeGit))
			Expect(gitSpec.SshConfig.SshPrivateKey).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.SshPrivateKey)).To(Equal(privateKey))
			Expect(gitSpec.SshConfig.PrivateKeyPassphrase).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.PrivateKeyPassphrase)).To(Equal(passphrase))
		})

		It("Create SSH repository without passphrase", func() {
			spec := api.RepositorySpec{}
			privateKey := "c3NoLXJzYSBBQUFBQjNOemFDMXljMkVBQUFBREFRQUJBQUFCZ1FDN2..." // base64 encoded
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "git@gitlab.com:myorg/myrepo.git",
				Type: api.GitRepoSpecTypeGit,
				SshConfig: &api.SshConfig{
					SshPrivateKey: &privateKey,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("ssh-repo-no-passphrase"),
					Labels: &map[string]string{"type": "ssh"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify SSH spec without passphrase
			gitSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(gitSpec.Url).To(Equal("git@gitlab.com:myorg/myrepo.git"))
			Expect(gitSpec.SshConfig.SshPrivateKey).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.SshPrivateKey)).To(Equal(privateKey))
			Expect(gitSpec.SshConfig.PrivateKeyPassphrase).To(BeNil())
		})

		It("Get SSH repository and verify fields", func() {
			spec := api.RepositorySpec{}
			privateKey := "c3NoLXJzYSBBQUFBQjNOemFDMXljMkVB..." // base64 encoded
			passphrase := "testpass"
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "git@github.com:testorg/testrepo.git",
				Type: api.GitRepoSpecTypeGit,
				SshConfig: &api.SshConfig{
					SshPrivateKey:        &privateKey,
					PrivateKeyPassphrase: &passphrase,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("ssh-get-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Get the repository by name
			repo, err := repositoryStore.Get(ctx, orgId, "ssh-get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("ssh-get-test"))

			// Verify SSH spec fields
			gitSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(gitSpec.Url).To(Equal("git@github.com:testorg/testrepo.git"))
			Expect(gitSpec.Type).To(Equal(api.GitRepoSpecTypeGit))
			Expect(gitSpec.SshConfig.SshPrivateKey).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.SshPrivateKey)).To(Equal(privateKey))
			Expect(gitSpec.SshConfig.PrivateKeyPassphrase).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.PrivateKeyPassphrase)).To(Equal(passphrase))
		})

		It("Update SSH repository", func() {
			// Create initial SSH repository
			spec := api.RepositorySpec{}
			privateKey := "c3NoLXJzYSBBQUFBQjNOemFDMXljMkVB..." // base64 encoded
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "git@github.com:original/repo.git",
				Type: api.GitRepoSpecTypeGit,
				SshConfig: &api.SshConfig{
					SshPrivateKey: &privateKey,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("ssh-update-test"),
				},
				Spec: spec,
			}
			_, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			// Update with new values
			newPrivateKey := "bmV3LXByaXZhdGUta2V5LWNvbnRlbnQ=" // base64 encoded
			newPassphrase := "newpassphrase"
			err = spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "git@github.com:updated/repo.git",
				Type: api.GitRepoSpecTypeGit,
				SshConfig: &api.SshConfig{
					SshPrivateKey:        &newPrivateKey,
					PrivateKeyPassphrase: &newPassphrase,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository.Spec = spec
			repo, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())

			// Verify updated values
			gitSpec, err := repo.Spec.AsGitRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(gitSpec.Url).To(Equal("git@github.com:updated/repo.git"))
			Expect(gitSpec.SshConfig.SshPrivateKey).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.SshPrivateKey)).To(Equal(newPrivateKey))
			Expect(gitSpec.SshConfig.PrivateKeyPassphrase).ToNot(BeNil())
			Expect(decryptString(*gitSpec.SshConfig.PrivateKeyPassphrase)).To(Equal(newPassphrase))
		})

		It("Delete SSH repository", func() {
			// Create SSH repository
			spec := api.RepositorySpec{}
			privateKey := "c3NoLXJzYSBBQUFBQjNOemFDMXljMkVB..."
			err := spec.FromGitRepoSpec(api.GitRepoSpec{
				Url:  "git@github.com:delete/repo.git",
				Type: api.GitRepoSpecTypeGit,
				SshConfig: &api.SshConfig{
					SshPrivateKey: &privateKey,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("ssh-delete-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Verify it exists
			_, err = repositoryStore.Get(ctx, orgId, "ssh-delete-test")
			Expect(err).ToNot(HaveOccurred())

			// Delete the repository
			eventCallbackCalled = false
			err = repositoryStore.Delete(ctx, orgId, "ssh-delete-test", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify it no longer exists
			_, err = repositoryStore.Get(ctx, orgId, "ssh-delete-test")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		// HTTP Repository CRUD tests
		It("Create HTTP repository with credentials", func() {
			spec := api.RepositorySpec{}
			username := "httpuser"
			password := "httppassword"
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://github.com/flightctl/flightctl.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					Username: &username,
					Password: &password,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("http-repo-with-creds"),
					Labels: &map[string]string{"type": "http"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))

			// Verify HTTP spec is preserved
			httpSpec, err := repo.Spec.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://github.com/flightctl/flightctl.git"))
			Expect(httpSpec.Type).To(Equal(api.HttpRepoSpecTypeHttp))
			Expect(httpSpec.HttpConfig.Username).ToNot(BeNil())
			Expect(*httpSpec.HttpConfig.Username).To(Equal(username))
			Expect(httpSpec.HttpConfig.Password).ToNot(BeNil())
			Expect(decryptString(*httpSpec.HttpConfig.Password)).To(Equal(password))
		})

		It("Create HTTP repository with token", func() {
			spec := api.RepositorySpec{}
			token := "ghp_1234567890abcdef"
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://github.com/flightctl/flightctl.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					Token: &token,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("http-repo-with-token"),
					Labels: &map[string]string{"type": "http"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify HTTP spec with token
			httpSpec, err := repo.Spec.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://github.com/flightctl/flightctl.git"))
			Expect(httpSpec.HttpConfig.Token).ToNot(BeNil())
			Expect(decryptString(*httpSpec.HttpConfig.Token)).To(Equal(token))
			Expect(httpSpec.HttpConfig.Username).To(BeNil())
			Expect(httpSpec.HttpConfig.Password).To(BeNil())
		})

		It("Create HTTP repository with TLS config", func() {
			spec := api.RepositorySpec{}
			caCrt := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t..." // base64 encoded
			tlsCrt := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t..."
			tlsKey := "LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0t..."
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://private.git.server/repo.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					CaCrt:  &caCrt,
					TlsCrt: &tlsCrt,
					TlsKey: &tlsKey,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("http-repo-with-tls"),
					Labels: &map[string]string{"type": "http"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify HTTP spec with TLS config
			httpSpec, err := repo.Spec.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://private.git.server/repo.git"))
			Expect(httpSpec.HttpConfig.CaCrt).ToNot(BeNil())
			Expect(*httpSpec.HttpConfig.CaCrt).To(Equal(caCrt))
			Expect(httpSpec.HttpConfig.TlsCrt).ToNot(BeNil())
			Expect(decryptString(*httpSpec.HttpConfig.TlsCrt)).To(Equal(tlsCrt))
			Expect(httpSpec.HttpConfig.TlsKey).ToNot(BeNil())
			Expect(decryptString(*httpSpec.HttpConfig.TlsKey)).To(Equal(tlsKey))
		})

		It("Create HTTP repository with skipServerVerification", func() {
			spec := api.RepositorySpec{}
			skipVerify := true
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://insecure.git.server/repo.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					SkipServerVerification: &skipVerify,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("http-repo-skip-verify"),
					Labels: &map[string]string{"type": "http"},
				},
				Spec:   spec,
				Status: nil,
			}

			repo, err := repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify HTTP spec with skipServerVerification
			httpSpec, err := repo.Spec.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://insecure.git.server/repo.git"))
			Expect(httpSpec.HttpConfig.SkipServerVerification).ToNot(BeNil())
			Expect(*httpSpec.HttpConfig.SkipServerVerification).To(BeTrue())
		})

		It("Get HTTP repository and verify fields", func() {
			spec := api.RepositorySpec{}
			username := "testuser"
			password := "testpass"
			validationSuffix := "/info/refs?service=git-upload-pack"
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:              "https://github.com/testorg/testrepo.git",
				Type:             api.HttpRepoSpecTypeHttp,
				ValidationSuffix: &validationSuffix,
				HttpConfig: &api.HttpConfig{
					Username: &username,
					Password: &password,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("http-get-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Get the repository by name
			repo, err := repositoryStore.Get(ctx, orgId, "http-get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("http-get-test"))

			// Verify HTTP spec fields
			httpSpec, err := repo.Spec.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://github.com/testorg/testrepo.git"))
			Expect(httpSpec.Type).To(Equal(api.HttpRepoSpecTypeHttp))
			Expect(httpSpec.ValidationSuffix).ToNot(BeNil())
			Expect(*httpSpec.ValidationSuffix).To(Equal(validationSuffix))
			Expect(httpSpec.HttpConfig.Username).ToNot(BeNil())
			Expect(*httpSpec.HttpConfig.Username).To(Equal(username))
			Expect(httpSpec.HttpConfig.Password).ToNot(BeNil())
			Expect(decryptString(*httpSpec.HttpConfig.Password)).To(Equal(password))
		})

		It("Update HTTP repository", func() {
			// Create initial HTTP repository
			spec := api.RepositorySpec{}
			token := "original-token"
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://github.com/original/repo.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					Token: &token,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("http-update-test"),
				},
				Spec: spec,
			}
			_, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			// Update with new values
			newToken := "new-updated-token"
			err = spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://github.com/updated/repo.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					Token: &newToken,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository.Spec = spec
			repo, created, err := repositoryStore.CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())

			// Verify updated values
			httpSpec, err := repo.Spec.AsHttpRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(httpSpec.Url).To(Equal("https://github.com/updated/repo.git"))
			Expect(httpSpec.HttpConfig.Token).ToNot(BeNil())
			Expect(decryptString(*httpSpec.HttpConfig.Token)).To(Equal(newToken))
		})

		It("Delete HTTP repository", func() {
			// Create HTTP repository
			spec := api.RepositorySpec{}
			token := "delete-test-token"
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://github.com/delete/repo.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					Token: &token,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("http-delete-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Verify it exists
			_, err = repositoryStore.Get(ctx, orgId, "http-delete-test")
			Expect(err).ToNot(HaveOccurred())

			// Delete the repository
			eventCallbackCalled = false
			err = repositoryStore.Delete(ctx, orgId, "http-delete-test", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify it no longer exists
			_, err = repositoryStore.Get(ctx, orgId, "http-delete-test")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("List HTTP repositories by type using FieldSelector", func() {
			// Create an HTTP repository
			spec := api.RepositorySpec{}
			token := "list-test-token"
			err := spec.FromHttpRepoSpec(api.HttpRepoSpec{
				Url:  "https://github.com/list-test/repo.git",
				Type: api.HttpRepoSpecTypeHttp,
				HttpConfig: &api.HttpConfig{
					Token: &token,
				},
			})
			Expect(err).ToNot(HaveOccurred())

			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("http-list-test"),
				},
				Spec: spec,
			}
			_, err = repositoryStore.Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List only HTTP type repositories
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.type": "http"}),
			}
			repositories, err := repositoryStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("http-list-test"))
		})

	})
})
