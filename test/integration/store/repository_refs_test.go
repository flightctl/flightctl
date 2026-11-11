package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
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

var _ = Describe("Repository reference sets", func() {
	var (
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		devStore   devicestore.Store
		fleetStore fleetstore.Store
		cfg        *config.Config
		db         *gorm.DB
		dbName     string
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		devStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		orgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), orgId)).To(Succeed())
		testutil.CreateTestDevices(ctx, 2, devStore, orgId, nil, false)
		testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet-1", &map[string]string{"key": "value"}, nil)
		repositoryStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		Expect(testutil.CreateRepositories(ctx, 3, repositoryStore, orgId)).To(Succeed())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	deviceRefs := func(name string) []string {
		repos, err := devStore.GetRepositoryRefs(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		names := lo.Map(repos.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
		slices.Sort(names)
		return names
	}
	fleetRefs := func(name string) []string {
		repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		names := lo.Map(repos.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
		slices.Sort(names)
		return names
	}
	repositoryRowExists := func(name string) bool {
		var count int64
		Expect(db.WithContext(ctx).Table("repositories").Where("org_id = ? AND name = ?", orgId, name).Count(&count).Error).To(Succeed())
		return count > 0
	}
	deviceGeneration := func(name string) int64 {
		device, err := devStore.Get(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return lo.FromPtr(device.Metadata.Generation)
	}
	failJoinTableWrites := func(table string) {
		callbackName := "test:fail-" + table
		Expect(db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == table {
				_ = tx.AddError(errors.New("injected join table failure"))
			}
		})).To(Succeed())
		DeferCleanup(func() {
			Expect(db.Callback().Create().Remove(callbackName)).To(Succeed())
		})
	}
	changeOsImage := func(image string) devicestore.DeviceApplyFunc {
		return func(m *devicestore.DeviceMutation) error {
			if err := m.RequireExisting(); err != nil {
				return err
			}
			if m.Device.Spec == nil {
				m.Device.Spec = &api.DeviceSpec{}
			}
			m.Device.Spec.Os = &api.DeviceOsSpec{Image: image}
			return nil
		}
	}
	withRefs := func(apply devicestore.DeviceApplyFunc, names ...string) devicestore.DeviceApplyFunc {
		return func(m *devicestore.DeviceMutation) error {
			if err := apply(m); err != nil {
				return err
			}
			refs := append([]string{}, names...)
			m.RepositoryRefs = &refs
			return nil
		}
	}

	Context("guarded device replacement", func() {
		It("When the generation matches it should replace the references with exactly the given set", func() {
			generation := deviceGeneration("mydevice-1")

			applied, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", generation, "myrepository-1", "myrepository-2", "myrepository-3")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeTrue())
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1", "myrepository-2", "myrepository-3"}))

			applied, err = devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", generation, "myrepository-1", "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeTrue())
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1", "myrepository-2"}), "a reference no longer in the set must be removed while shared ones stay")
		})

		It("When the generation is stale it should leave the references unchanged", func() {
			stale := deviceGeneration("mydevice-1")
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", stale, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			_, _, _, err = devStore.Mutate(ctx, orgId, "mydevice-1", nil, changeOsImage("quay.io/example/os:2"))
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceGeneration("mydevice-1")).To(Equal(stale + 1))

			applied, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", stale, "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeFalse())
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1"}))
		})

		It("When the device is owned by a fleet it should not write references", func() {
			testutil.CreateTestDevice(ctx, devStore, orgId, "owned-1", lo.ToPtr("Fleet/myfleet-1"), nil, nil)

			applied, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "owned-1", deviceGeneration("owned-1"), "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeFalse())
			Expect(deviceRefs("owned-1")).To(BeEmpty())
		})

		It("When the device does not exist it should report not found", func() {
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "missing", 1, "myrepository-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("When the device belongs to another organization it should report not found", func() {
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, uuid.New(), "mydevice-1", 1, "myrepository-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(deviceRefs("mydevice-1")).To(BeEmpty())
		})

		It("When given an empty set it should remove every reference", func() {
			generation := deviceGeneration("mydevice-1")
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", generation, "myrepository-1", "myrepository-2")
			Expect(err).ToNot(HaveOccurred())

			applied, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", generation)
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeTrue())
			Expect(deviceRefs("mydevice-1")).To(BeEmpty())
		})

		It("When writing the references fails it should keep the previous references and create no placeholder rows", func() {
			generation := deviceGeneration("mydevice-1")
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", generation, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			failJoinTableWrites("device_repos")

			_, err = devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", generation, "myrepository-2", "not-created-yet")
			Expect(err).To(HaveOccurred())
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1"}))
			Expect(repositoryRowExists("not-created-yet")).To(BeFalse())
		})
	})

	Context("device mutations with references", func() {
		It("When a mutation carries references it should commit the device change and the references together", func() {
			generation := deviceGeneration("mydevice-1")

			updated, _, created, err := devStore.Mutate(ctx, orgId, "mydevice-1", nil, withRefs(changeOsImage("quay.io/example/os:2"), "myrepository-1", "myrepository-2"))
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeFalse())
			Expect(lo.FromPtr(updated.Metadata.Generation)).To(Equal(generation + 1))
			stored, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(stored.Spec.Os.Image).To(Equal("quay.io/example/os:2"))
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1", "myrepository-2"}))
		})

		It("When a mutation carries only references it should write them without changing the generation", func() {
			generation := deviceGeneration("mydevice-1")

			_, _, _, err := devStore.Mutate(ctx, orgId, "mydevice-1", nil, func(m *devicestore.DeviceMutation) error {
				if err := m.RequireExisting(); err != nil {
					return err
				}
				refs := []string{"myrepository-2"}
				m.RepositoryRefs = &refs
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceGeneration("mydevice-1")).To(Equal(generation))
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-2"}))
		})

		It("When a mutation carries no references it should leave the stored references unchanged", func() {
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", deviceGeneration("mydevice-1"), "myrepository-1")
			Expect(err).ToNot(HaveOccurred())

			_, _, _, err = devStore.Mutate(ctx, orgId, "mydevice-1", nil, changeOsImage("quay.io/example/os:2"))
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1"}))
		})

		It("When a mutation carries an empty set it should remove every reference", func() {
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", deviceGeneration("mydevice-1"), "myrepository-1")
			Expect(err).ToNot(HaveOccurred())

			_, _, _, err = devStore.Mutate(ctx, orgId, "mydevice-1", nil, withRefs(changeOsImage("quay.io/example/os:2")))
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceRefs("mydevice-1")).To(BeEmpty())
		})

		It("When a mutation creates a device with references it should create both", func() {
			device := testutil.ReturnTestDevice(orgId, "newdevice", nil, nil, nil)
			_, _, created, err := devStore.Mutate(ctx, orgId, "newdevice", nil, func(m *devicestore.DeviceMutation) error {
				m.Device = &device
				refs := []string{"myrepository-3"}
				m.RepositoryRefs = &refs
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(deviceRefs("newdevice")).To(Equal([]string{"myrepository-3"}))
		})

		It("When writing the references fails it should roll back the device change, the references and placeholder rows", func() {
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", deviceGeneration("mydevice-1"), "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			before, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			failJoinTableWrites("device_repos")

			_, _, _, err = devStore.Mutate(ctx, orgId, "mydevice-1", nil, withRefs(changeOsImage("quay.io/example/os:2"), "myrepository-2", "not-created-yet"))
			Expect(err).To(HaveOccurred())

			after, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(after.Spec).To(Equal(before.Spec))
			Expect(after.Metadata.Generation).To(Equal(before.Metadata.Generation))
			Expect(after.Metadata.ResourceVersion).To(Equal(before.Metadata.ResourceVersion))
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1"}))
			Expect(repositoryRowExists("not-created-yet")).To(BeFalse())
		})

		It("When creating a device with references fails it should not create the device", func() {
			failJoinTableWrites("device_repos")
			device := testutil.ReturnTestDevice(orgId, "newdevice", nil, nil, nil)

			_, _, _, err := devStore.Mutate(ctx, orgId, "newdevice", nil, func(m *devicestore.DeviceMutation) error {
				m.Device = &device
				refs := []string{"not-created-yet"}
				m.RepositoryRefs = &refs
				return nil
			})
			Expect(err).To(HaveOccurred())
			_, err = devStore.Get(ctx, orgId, "newdevice")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(repositoryRowExists("not-created-yet")).To(BeFalse())
		})

		It("When the device changes between load and persist it should retry with a fresh load and commit both writes", func() {
			attempts := 0
			_, _, _, err := devStore.Mutate(ctx, orgId, "mydevice-1", nil, func(m *devicestore.DeviceMutation) error {
				attempts++
				if attempts == 1 {
					_, _, _, concurrentErr := devStore.Mutate(ctx, orgId, "mydevice-1", nil, func(c *devicestore.DeviceMutation) error {
						c.Device.Metadata.Labels = &map[string]string{"concurrent": "true"}
						return nil
					})
					Expect(concurrentErr).ToNot(HaveOccurred())
				}
				return withRefs(changeOsImage("quay.io/example/os:3"), "myrepository-2")(m)
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(attempts).To(Equal(2), "the conflicting write must force exactly one reload")

			stored, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(stored.Spec.Os.Image).To(Equal("quay.io/example/os:3"))
			Expect(lo.FromPtr(stored.Metadata.Labels)).To(HaveKeyWithValue("concurrent", "true"))
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-2"}))
		})

		It("When a stale writer races with an admission write it should never replace the fresh references", func() {
			for i := 0; i < 10; i++ {
				stale := deviceGeneration("mydevice-1")
				fresh := fmt.Sprintf("quay.io/example/os:%d", i)
				var wg sync.WaitGroup
				var admissionErr, staleErr error
				start := make(chan struct{})
				wg.Add(2)
				go func() {
					defer wg.Done()
					defer GinkgoRecover()
					<-start
					_, _, _, admissionErr = devStore.Mutate(ctx, orgId, "mydevice-1", nil, withRefs(changeOsImage(fresh), "myrepository-1", "myrepository-2"))
				}()
				go func() {
					defer wg.Done()
					defer GinkgoRecover()
					<-start
					_, staleErr = devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", stale, "myrepository-3")
				}()
				close(start)
				wg.Wait()

				Expect(admissionErr).ToNot(HaveOccurred())
				Expect(staleErr).ToNot(HaveOccurred())
				Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1", "myrepository-2"}), "iteration %d", i)
			}
		})
	})

	Context("device creation with references in one transaction", func() {
		createWithRefs := func(name string, refs ...string) error {
			return devStore.WithTransaction(ctx, func(txCtx context.Context) error {
				device := testutil.ReturnTestDevice(orgId, name, nil, nil, nil)
				created, err := devStore.Create(txCtx, orgId, &device, nil)
				if err != nil {
					return err
				}
				applied, err := devStore.ReplaceRepositoryRefsIfCurrent(txCtx, orgId, name, lo.FromPtr(created.Metadata.Generation), refs...)
				if err != nil {
					return err
				}
				if !applied {
					return errors.New("references were not applied to the new device")
				}
				return nil
			})
		}

		It("When the device and its references are written in one transaction it should commit both", func() {
			Expect(createWithRefs("newdevice", "myrepository-1", "myrepository-2")).To(Succeed())
			Expect(deviceRefs("newdevice")).To(Equal([]string{"myrepository-1", "myrepository-2"}))
		})

		It("When writing the references fails it should not create the device", func() {
			failJoinTableWrites("device_repos")

			Expect(createWithRefs("newdevice", "not-created-yet")).ToNot(Succeed())
			_, err := devStore.Get(ctx, orgId, "newdevice")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(repositoryRowExists("not-created-yet")).To(BeFalse())
		})

		It("When the name already exists it should report a duplicate and leave the existing references", func() {
			_, err := devStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "mydevice-1", deviceGeneration("mydevice-1"), "myrepository-1")
			Expect(err).ToNot(HaveOccurred())

			Expect(createWithRefs("mydevice-1", "myrepository-3")).To(MatchError(flterrors.ErrDuplicateName))
			Expect(deviceRefs("mydevice-1")).To(Equal([]string{"myrepository-1"}))
		})
	})

	Context("guarded fleet replacement", func() {
		fleetGeneration := func() int64 {
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			return lo.FromPtr(fleet.Metadata.Generation)
		}

		It("When the generation matches it should replace the fleet references with exactly the given set", func() {
			generation := fleetGeneration()
			applied, err := fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "myfleet-1", generation, "myrepository-1", "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeTrue())
			Expect(fleetRefs("myfleet-1")).To(Equal([]string{"myrepository-1", "myrepository-2"}))

			applied, err = fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "myfleet-1", generation, "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeTrue())
			Expect(fleetRefs("myfleet-1")).To(Equal([]string{"myrepository-2"}))
		})

		It("When the generation is stale it should leave the fleet references unchanged", func() {
			stale := fleetGeneration()
			_, err := fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "myfleet-1", stale, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			_, _, _, err = fleetStore.Mutate(ctx, orgId, "myfleet-1", nil, func(m *fleetstore.FleetMutation) error {
				m.Fleet.Spec.Template.Spec.Os = &api.DeviceOsSpec{Image: "quay.io/example/os:2"}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(fleetGeneration()).To(Equal(stale + 1))

			applied, err := fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "myfleet-1", stale, "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(applied).To(BeFalse())
			Expect(fleetRefs("myfleet-1")).To(Equal([]string{"myrepository-1"}))
		})

		It("When the fleet does not exist it should report not found", func() {
			_, err := fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "missing", 1, "myrepository-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("When writing the fleet references fails it should keep the previous references and create no placeholder rows", func() {
			generation := fleetGeneration()
			_, err := fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "myfleet-1", generation, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			failJoinTableWrites("fleet_repos")

			_, err = fleetStore.ReplaceRepositoryRefsIfCurrent(ctx, orgId, "myfleet-1", generation, "myrepository-2", "not-created-yet")
			Expect(err).To(HaveOccurred())
			Expect(fleetRefs("myfleet-1")).To(Equal([]string{"myrepository-1"}))
			Expect(repositoryRowExists("not-created-yet")).To(BeFalse())
		})
	})
})
