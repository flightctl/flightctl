package resourcesync

import (
	"context"
	"fmt"
	"io"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ResourceSync Suite")
}

var repo model.Repository = model.Repository{
	Spec: &model.JSONField[api.RepositorySpec]{
		Data: api.RepositorySpec{
			Repo: util.StrToPtr("https://github.com/flightctl/flightctl"),
		},
	},
}
var _ = Describe("ResourceSync", Ordered, func() {
	var (
		log          *logrus.Logger
		ctx          context.Context
		orgId        uuid.UUID
		cfg          *config.Config
		stores       store.Store
		dbName       string
		resourceSync *ResourceSync
		//hash         string
		memfs billy.Filesystem
	)

	BeforeAll(func() {
		// Clone the repo
		fs, _, err := resourceSync.cloneRepo(&repo, nil)
		Expect(err).ToNot(HaveOccurred())
		memfs = fs

		err = fs.MkdirAll("/fleets", 0666)
		Expect(err).ToNot(HaveOccurred())

		fleet1, err := fs.Open("/examples/fleet.yaml")
		Expect(err).ToNot(HaveOccurred())
		defer fleet1.Close()
		fleet2, err := fs.Open("/examples/fleet-b.yaml")
		Expect(err).ToNot(HaveOccurred())
		defer fleet2.Close()
		f1, err := fs.Create("/fleets/f1.yaml")
		Expect(err).ToNot(HaveOccurred())
		defer f1.Close()
		f2, err := fs.Create("/fleets/f2.yaml")
		Expect(err).ToNot(HaveOccurred())
		defer f2.Close()

		_, err = io.Copy(f1, fleet1)
		Expect(err).ToNot(HaveOccurred())
		_, err = io.Copy(f2, fleet2)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		stores, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		resourceSync = NewResourceSync(log, stores)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, stores, dbName)
	})

	Context("ResourceSync tests", func() {
		It("parseAndValidate", func() {
			rs := model.ResourceSync{
				Spec: &model.JSONField[api.ResourceSyncSpec]{
					Data: api.ResourceSyncSpec{
						Repository: util.StrToPtr("demoRepo"),
						Path:       util.StrToPtr("/examples"),
					},
				},
			}
			_, err := resourceSync.parseAndValidateResources(ctx, &rs, &repo)
			Expect(err).To(HaveOccurred())

			rs.Spec.Data.Path = util.StrToPtr("/examples/fleet.yaml")
			resources, err := resourceSync.parseAndValidateResources(ctx, &rs, &repo)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(1))
			Expect(resources[0]["kind"]).To(Equal(model.FleetKind))
		})
		It("Parse generic resources", func() {
			genericResources, err := resourceSync.extractResourcesFromFile(orgId.String(), memfs, "/examples/fleet.yaml")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(genericResources)).To(Equal(1))
			Expect(genericResources[0]["kind"]).To(Equal(model.FleetKind))

			genericResources, err = resourceSync.extractResourcesFromDir(orgId.String(), memfs, "/fleets")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(genericResources)).To(Equal(2))
			Expect(genericResources[0]["kind"]).To(Equal(model.FleetKind))

			// Dir contains fleets and other resources
			_, err = resourceSync.extractResourcesFromDir(orgId.String(), memfs, "/examples/")
			Expect(err).To(HaveOccurred())

			// File is not a fleet
			_, err = resourceSync.extractResourcesFromFile(orgId.String(), memfs, "/examples/device.yaml")
			Expect(err).To(HaveOccurred())
		})
		It("parse fleet", func() {
			genericResources, err := resourceSync.extractResourcesFromFile(orgId.String(), memfs, "/examples/fleet.yaml")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(genericResources)).To(Equal(1))
			fleets, err := resourceSync.parseFleets(genericResources, orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets)).To(Equal(1))
			fleet := fleets[0]
			Expect(fleet.Kind).To(Equal(model.FleetKind))
			Expect(*fleet.Metadata.Name).To(Equal("default"))
			Expect(fleet.Spec.Selector.MatchLabels["fleet"]).To(Equal("default"))

			// change the kind and parse
			genericResources[0]["kind"] = "NotValid"
			_, err = resourceSync.parseFleets(genericResources, orgId)
			Expect(err).To(HaveOccurred())

			genericResources, err = resourceSync.extractResourcesFromDir(orgId.String(), memfs, "/fleets")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(genericResources)).To(Equal(2))
			fleets, err = resourceSync.parseFleets(genericResources, orgId)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets)).To(Equal(2))
		})
		It("Should run sync", func() {
			rs := model.ResourceSync{
				Spec: &model.JSONField[api.ResourceSyncSpec]{
					Data: api.ResourceSyncSpec{
						Repository: util.StrToPtr("demoRepo"),
						Path:       util.StrToPtr("/examples"),
					},
				},
			}

			// No hash, path, and sync condition in status
			willRunSync := shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			rs.Status = &model.JSONField[api.ResourceSyncStatus]{
				Data: api.ResourceSyncStatus{
					LastSyncedCommitHash: util.StrToPtr("hash"),
					LastSyncedPath:       util.StrToPtr("/examplesx"),
				},
			}
			// Sync condition is true
			addSyncedCondition(&rs, nil)
			// hash good, path not
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			// not same hash
			willRunSync = shouldRunSync("hashx", rs)
			Expect(willRunSync).To(BeTrue())

			// not same path
			rs.Status.Data.LastSyncedPath = util.StrToPtr("/examples")
			willRunSync = shouldRunSync("hashx", rs)
			Expect(willRunSync).To(BeTrue())

			// sync condition false
			addSyncedCondition(&rs, fmt.Errorf("some error"))
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())
		})
		It("File validation", func() {
			Expect(isValidFile("something")).To(BeFalse())
			Expect(isValidFile("something.pdf")).To(BeFalse())
			for _, ext := range fileExtensions {
				Expect(isValidFile(fmt.Sprintf("file.%s", ext))).To(BeTrue())
			}
		})
		It("Set conditions", func() {
			rs := model.ResourceSync{
				Spec: &model.JSONField[api.ResourceSyncSpec]{
					Data: api.ResourceSyncSpec{
						Repository: util.StrToPtr("demoRepo"),
						Path:       util.StrToPtr("/examples"),
					},
				},
			}

			addRepoNotFoundCondition(&rs, nil)
			Expect(len(*rs.Status.Data.Conditions)).To(Equal(1))

			conditions, condition := extractPrevConditionByType(&rs, accessibleConditionType)
			Expect(len(conditions)).To(Equal(0))
			Expect(condition).ToNot(BeNil())

			// Override access cond
			addRepoNotFoundCondition(&rs, fmt.Errorf("Error"))
			Expect(len(*rs.Status.Data.Conditions)).To(Equal(1))
			Expect(len(conditions)).To(Equal(0))
			Expect(condition).ToNot(BeNil())

			addRepoAccessCondition(&rs, nil)
			Expect(len(conditions)).To(Equal(0))
			Expect(condition).ToNot(BeNil())
			Expect(len(*rs.Status.Data.Conditions)).To(Equal(1))

			addPathAccessCondition(&rs, nil)
			Expect(len(conditions)).To(Equal(0))
			Expect(condition).ToNot(BeNil())
			Expect(len(*rs.Status.Data.Conditions)).To(Equal(1))

			// Add other conditions
			addResourceParseCondition(&rs, nil)
			Expect(len(*rs.Status.Data.Conditions)).To(Equal(2))
			addSyncedCondition(&rs, nil)
			Expect(len(*rs.Status.Data.Conditions)).To(Equal(3))

		})
	})
})
