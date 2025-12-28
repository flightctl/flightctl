package store_test

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

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
		storeInst           store.Store
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
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(ctx, log)
		eventCallbackCalled = false
		eventCallback = store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
			eventCallbackCalled = true
		})

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		err = testutil.CreateRepositories(ctx, 3, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		nilrepo := model.Repository{Resource: model.Resource{OrgID: orgId, Name: "nilspec"}}
		result := db.WithContext(ctx).Create(&nilrepo)
		Expect(result.Error).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	Context("Repository store", func() {
		It("Get repository success", func() {
			repo, err := storeInst.Repository().Get(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("myrepository-1"))
		})

		It("Get repository - not found error", func() {
			_, err := storeInst.Repository().Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get repository - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := storeInst.Repository().Get(ctx, badOrgId, "myrepository-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get repository - nil spec - not found error", func() {
			_, err := storeInst.Repository().Get(ctx, orgId, "nilspec")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Delete repository success", func() {
			err := storeInst.Repository().Delete(ctx, orgId, "myrepository-1", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
		})

		It("Delete repository success when not found", func() {
			err := storeInst.Repository().Delete(ctx, orgId, "nonexistent", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeFalse())
		})

		It("Delete repository success when nil spec", func() {
			err := storeInst.Repository().Delete(ctx, orgId, "nilspec", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeFalse())
		})

		It("List with paging", func() {
			// Delete the repo with nilspec so it doesn't interfere with the counts
			nilrepo := model.Repository{Resource: model.Resource{OrgID: orgId, Name: "nilspec"}}
			result := db.WithContext(ctx).Delete(&nilrepo)
			Expect(result.Error).ToNot(HaveOccurred())

			listParams := store.ListParams{Limit: 1000}
			allRepositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allRepositories.Items)).To(Equal(numRepositories))
			allRepoNames := make([]string, len(allRepositories.Items))
			for i, repo := range allRepositories.Items {
				allRepoNames[i] = *repo.Metadata.Name
			}

			foundRepoNames := make([]string, len(allRepositories.Items))
			listParams.Limit = 1
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundRepoNames[0] = *repositories.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(repositories.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundRepoNames[1] = *repositories.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(repositories.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
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
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("myrepository-1"))
		})

		It("CreateOrUpdateRepository create mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "myrepo",
				Type: "git",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateRepository update mode", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "myotherrepo",
				Type: "git",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("myrepository-1"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(created).To(Equal(false))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("CreateOrUpdateRepository create nilspec", func() {
			spec := api.RepositorySpec{}
			err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
				Url:  "myotherrepo",
				Type: "git",
			})
			Expect(err).ToNot(HaveOccurred())
			repository := api.Repository{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("nilspec"),
				},
				Spec:   spec,
				Status: nil,
			}
			repo, created, err := storeInst.Repository().CreateOrUpdate(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(created).To(Equal(true))
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))
			repoSpec, err := repo.Spec.AsGenericRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(repoSpec.Url).To(Equal("myotherrepo"))
			Expect(repo.Status.Conditions).ToNot(BeNil())
			Expect(repo.Status.Conditions).To(BeEmpty())
		})

		It("Delete repo with fleet association", func() {
			testutil.CreateTestFleets(ctx, 1, storeInst.Fleet(), orgId, "myfleet", false, nil)

			err := storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "myfleet-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = storeInst.Repository().Delete(ctx, orgId, "myrepository-1", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
		})

		It("Delete repo with device association", func() {
			testutil.CreateTestDevices(ctx, 1, storeInst.Device(), orgId, nil, false)

			err := storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = storeInst.Repository().Delete(ctx, orgId, "myrepository-1", eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
		})

		It("CountByOrg - with specific orgId", func() {
			// Test with specific orgId
			results, err := storeInst.Repository().CountByOrg(ctx, &orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].OrgID).To(Equal(orgId.String()))
			Expect(results[0].Count).To(Equal(int64(4))) // 4 repositories from BeforeEach

			// Create additional repositories in the same org with unique names
			for i := 10; i <= 11; i++ {
				spec := api.RepositorySpec{}
				err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
					Url: "myrepo",
				})
				Expect(err).ToNot(HaveOccurred())

				resource := api.Repository{
					Metadata: api.ObjectMeta{
						Name:   lo.ToPtr(fmt.Sprintf("myrepository-%d", i)),
						Labels: &map[string]string{"environment": "test"},
					},
					Spec: spec,
				}
				_, err = storeInst.Repository().Create(ctx, orgId, &resource, eventCallback)
				Expect(err).ToNot(HaveOccurred())
			}

			results, err = storeInst.Repository().CountByOrg(ctx, &orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].OrgID).To(Equal(orgId.String()))
			Expect(results[0].Count).To(Equal(int64(6))) // 4 original + 2 new
		})

		It("CountByOrg - with nil orgId (all orgs)", func() {
			// Create repositories in another organization
			otherOrgId := uuid.New()
			err := testutil.CreateTestOrganization(ctx, storeInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())
			err = testutil.CreateRepositories(ctx, 2, storeInst, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			// Test with nil orgId (should get all orgs)
			results, err := storeInst.Repository().CountByOrg(ctx, nil)
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

			repo, err := storeInst.Repository().Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())
			Expect(repo.ApiVersion).To(Equal(model.RepositoryAPIVersion()))
			Expect(repo.Kind).To(Equal(api.RepositoryKind))

			// Verify OCI spec is preserved
			ociSpec, err := repo.Spec.GetOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("quay.io"))
			Expect(ociSpec.Type).To(Equal(api.RepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.ReadWrite))
			Expect(ociSpec.OciAuth).ToNot(BeNil())
			dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
			Expect(err).ToNot(HaveOccurred())
			Expect(dockerAuth.Username).To(Equal("myuser"))
			Expect(dockerAuth.Password).To(Equal("mypassword"))
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

			repo, err := storeInst.Repository().Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(eventCallbackCalled).To(BeTrue())

			// Verify OCI spec without credentials
			ociSpec, err := repo.Spec.GetOciRepoSpec()
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
			_, err = storeInst.Repository().Create(ctx, orgId, &repoRw, eventCallback)
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
			_, err = storeInst.Repository().Create(ctx, orgId, &repoR, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List only read-write repositories
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.accessMode": "ReadWrite"}),
			}
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-output-registry"))

			// Verify the returned repository has correct OCI spec
			ociSpec, err := repositories.Items[0].Spec.GetOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("quay.io"))
			Expect(ociSpec.Type).To(Equal(api.RepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.ReadWrite))

			// List only read-only repositories
			listParams.FieldSelector = selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.accessMode": "Read"})
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-input-registry"))

			// Verify the returned repository has correct OCI spec
			ociSpec, err = repositories.Items[0].Spec.GetOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("registry.redhat.io"))
			Expect(ociSpec.Type).To(Equal(api.RepoSpecTypeOci))
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
			_, err = storeInst.Repository().Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// Get the repository by name
			repo, err := storeInst.Repository().Get(ctx, orgId, "oci-get-test")
			Expect(err).ToNot(HaveOccurred())
			Expect(*repo.Metadata.Name).To(Equal("oci-get-test"))

			// Verify OCI spec fields including accessMode
			ociSpec, err := repo.Spec.GetOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(ociSpec.Registry).To(Equal("quay.io"))
			Expect(ociSpec.Type).To(Equal(api.RepoSpecTypeOci))
			Expect(*ociSpec.AccessMode).To(Equal(api.ReadWrite))
			Expect(ociSpec.OciAuth).ToNot(BeNil())
			dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
			Expect(err).ToNot(HaveOccurred())
			Expect(dockerAuth.Username).To(Equal("testuser"))
			Expect(dockerAuth.Password).To(Equal("testpass"))
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
			_, err = storeInst.Repository().Create(ctx, orgId, &repoOciRead, eventCallback)
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
			_, err = storeInst.Repository().Create(ctx, orgId, &repoOciRw, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List with combined filter: type=oci AND accessMode=ReadWrite
			listParams := store.ListParams{
				Limit: 1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{
					"spec.type":       "oci",
					"spec.accessMode": "ReadWrite",
				}),
			}
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-combined-rw"))

			// List with combined filter: type=oci AND accessMode=Read
			listParams.FieldSelector = selector.NewFieldSelectorFromMapOrDie(map[string]string{
				"spec.type":       "oci",
				"spec.accessMode": "Read",
			})
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
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
			_, err = storeInst.Repository().Create(ctx, orgId, &repository, eventCallback)
			Expect(err).ToNot(HaveOccurred())

			// List only OCI type repositories
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.type": "oci"}),
			}
			repositories, err := storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(1))
			Expect(*repositories.Items[0].Metadata.Name).To(Equal("oci-type-test"))

			// List only git type repositories (existing ones from BeforeEach)
			listParams.FieldSelector = selector.NewFieldSelectorFromMapOrDie(map[string]string{"spec.type": "git"})
			repositories, err = storeInst.Repository().List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(repositories.Items)).To(Equal(numRepositories)) // Original git repos
		})

	})
})
