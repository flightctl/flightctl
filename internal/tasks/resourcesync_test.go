package tasks

import (
	"context"
	"fmt"
	"io"

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
		memfs       billy.Filesystem
		taskManager TaskManager
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
		stores, cfg, dbName = store.PrepareDBForUnitTests(log)
		taskManager = Init(log, stores)
		resourceSync = NewResourceSync(taskManager)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, stores, dbName)
	})

	Context("ResourceSync tests", func() {
		It("parseAndValidate", func() {
			rs := model.ResourceSync{
				Resource: model.Resource{
					Generation: util.Int64ToPtr(1),
				},
				Spec: &model.JSONField[api.ResourceSyncSpec]{
					Data: api.ResourceSyncSpec{
						Repository: util.StrToPtr("demoRepo"),
						Path:       util.StrToPtr("/examples"),
					},
				},
			}
			_, err := resourceSync.parseAndValidateResources(ctx, &rs, &repo)
			// Have unsupported resources in folder
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
			owner := util.StrToPtr("ResourceSync/foo")

			genericResources, err := resourceSync.extractResourcesFromFile(orgId.String(), memfs, "/examples/fleet.yaml")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(genericResources)).To(Equal(1))
			fleets, err := resourceSync.parseFleets(genericResources, orgId, owner)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets)).To(Equal(1))
			fleet := fleets[0]
			Expect(fleet.Kind).To(Equal(model.FleetKind))
			Expect(*fleet.Metadata.Name).To(Equal("default"))
			Expect(fleet.Spec.Selector.MatchLabels["fleet"]).To(Equal("default"))

			// change the kind and parse
			genericResources[0]["kind"] = "NotValid"
			_, err = resourceSync.parseFleets(genericResources, orgId, owner)
			Expect(err).To(HaveOccurred())

			genericResources, err = resourceSync.extractResourcesFromDir(orgId.String(), memfs, "/fleets")
			Expect(err).ToNot(HaveOccurred())
			Expect(len(genericResources)).To(Equal(2))
			fleets, err = resourceSync.parseFleets(genericResources, orgId, owner)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(fleets)).To(Equal(2))
			Expect(fleets[0].Metadata.Owner).ToNot(BeNil())
			Expect(*fleets[0].Metadata.Owner).To(Equal(*owner))
		})

		It("delta calc", func() {
			owner := "ResourceSync/foo"
			ownedFleets := []api.Fleet{
				{
					Metadata: api.ObjectMeta{
						Name:  util.StrToPtr("fleet-1"),
						Owner: util.StrToPtr(owner),
					},
				},
				{
					Metadata: api.ObjectMeta{
						Name:  util.StrToPtr("fleet-2"),
						Owner: util.StrToPtr(owner),
					},
				},
			}
			newFleets := []*api.Fleet{
				&ownedFleets[1],
			}

			delta := resourceSync.fleetsDelta(ownedFleets, newFleets)
			Expect(len(delta)).To(Equal(1))
			Expect(delta[0]).To(Equal("fleet-1"))
		})
		It("Should run sync", func() {
			rs := model.ResourceSync{
				Resource: model.Resource{
					Generation: util.Int64ToPtr(1),
				},
				Spec: &model.JSONField[api.ResourceSyncSpec]{
					Data: api.ResourceSyncSpec{
						Repository: util.StrToPtr("demoRepo"),
						Path:       util.StrToPtr("/examples"),
					},
				},
			}

			// no status - should run sync
			willRunSync := shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			rs.Status = &model.JSONField[api.ResourceSyncStatus]{
				Data: api.ResourceSyncStatus{
					ObservedCommit:     util.StrToPtr("old"),
					ObservedGeneration: util.Int64ToPtr(1),
				},
			}
			// hash changed - should run
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			// Observed generation not up do date - should run sync
			rs.Status.Data.ObservedCommit = util.StrToPtr("hash")
			rs.Status.Data.ObservedGeneration = util.Int64ToPtr(0)
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			// Generation and commit fine, but no sync condition
			rs.Status.Data.ObservedGeneration = util.Int64ToPtr(1)
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			// Sync condition false - should run sync
			addSyncedCondition(&rs, fmt.Errorf("Some error"))
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeTrue())

			// No need to run. all up to date
			addSyncedCondition(&rs, nil)
			willRunSync = shouldRunSync("hash", rs)
			Expect(willRunSync).To(BeFalse())
		})
		It("File validation", func() {
			Expect(isValidFile("something")).To(BeFalse())
			Expect(isValidFile("something.pdf")).To(BeFalse())
			for _, ext := range fileExtensions {
				Expect(isValidFile(fmt.Sprintf("file.%s", ext))).To(BeTrue())
			}
		})
	})
})
