package kvstore_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "KVstore Suite")
}

var _ = Describe("FleetSelector", func() {
	var (
		ctx     context.Context
		orgId   uuid.UUID
		kvStore kvstore.KVStore
		log     *logrus.Logger
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		cfg := config.NewDefault()
		cfg.KV.Username = "flightctl"
		cfg.KV.Password = "flightctl-redis-pass"

		var err error
		kvStore, err = kvstore.NewKVStore(ctx, log, cfg.KV)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		err := kvStore.DeleteAllKeys(ctx)
		Expect(err).ToNot(HaveOccurred())
		kvStore.Close()
	})

	When("fetching a git revision", func() {
		It("returns what is stored if the key exists", func() {
			key := kvstore.GitRevisionKey{
				OrgID:           orgId,
				Fleet:           "myfleet",
				TemplateVersion: "mytv",
				Repository:      "myrepo",
				TargetRevision:  "main",
			}

			updated, err := kvStore.SetNX(ctx, key.ComposeKey(), []byte("abc123"))
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())

			updated, err = kvStore.SetNX(ctx, key.ComposeKey(), []byte("def456"))
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeFalse())

			hash, err := kvStore.Get(ctx, key.ComposeKey())
			Expect(err).ToNot(HaveOccurred())
			Expect(hash).To(Equal([]byte("abc123")))
		})

		It("returns an empty string if the key doesn't exist", func() {
			key := kvstore.GitRevisionKey{
				OrgID:           orgId,
				Fleet:           "myfleet",
				TemplateVersion: "mytv",
				Repository:      "myrepo",
				TargetRevision:  "main",
			}

			hash, err := kvStore.Get(ctx, key.ComposeKey())
			Expect(err).ToNot(HaveOccurred())
			Expect(hash).To(HaveLen(0))
		})
	})

	When("setting a repo URL", func() {
		It("stores what is passed if the key doesn't exist", func() {
			key := kvstore.RepositoryUrlKey{
				OrgID:           orgId,
				Fleet:           "myfleet",
				TemplateVersion: "mytv",
				Repository:      "myrepo",
			}

			url, err := kvStore.GetOrSetNX(ctx, key.ComposeKey(), []byte("https://myurl"))
			Expect(err).ToNot(HaveOccurred())
			Expect(url).To(Equal([]byte("https://myurl")))
		})

		It("returns what is stored if the key exists", func() {
			key := kvstore.RepositoryUrlKey{
				OrgID:           orgId,
				Fleet:           "myfleet",
				TemplateVersion: "mytv",
				Repository:      "myrepo",
			}

			url, err := kvStore.GetOrSetNX(ctx, key.ComposeKey(), []byte("https://myurl"))
			Expect(err).ToNot(HaveOccurred())
			Expect(url).To(Equal([]byte("https://myurl")))

			url, err = kvStore.GetOrSetNX(ctx, key.ComposeKey(), []byte("https://otherurl"))
			Expect(err).ToNot(HaveOccurred())
			Expect(url).To(Equal([]byte("https://myurl")))
		})
	})

	When("deleting a TemplateVersion", func() {
		It("deletes all its related keys", func() {
			key := kvstore.RepositoryUrlKey{
				OrgID:           orgId,
				Fleet:           "myfleet",
				TemplateVersion: "mytv",
				Repository:      "myrepo",
			}

			updated, err := kvStore.SetNX(ctx, key.ComposeKey(), []byte("https://myurl"))
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())
			key.TemplateVersion = "othertv"
			updated, err = kvStore.SetNX(ctx, key.ComposeKey(), []byte("https://otherurl"))
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())

			key2 := kvstore.GitRevisionKey{
				OrgID:           orgId,
				Fleet:           "myfleet",
				TemplateVersion: "mytv",
				Repository:      "myrepo",
				TargetRevision:  "main",
			}

			updated, err = kvStore.SetNX(ctx, key2.ComposeKey(), []byte("abc123"))
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())
			key2.TemplateVersion = "othertv"
			updated, err = kvStore.SetNX(ctx, key2.ComposeKey(), []byte("def456"))
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeTrue())

			tvkey := kvstore.TemplateVersionKey{OrgID: orgId, Fleet: "myfleet", TemplateVersion: "mytv"}
			err = kvStore.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
			Expect(err).ToNot(HaveOccurred())

			ret, err := kvStore.Get(ctx, key.ComposeKey())
			Expect(err).ToNot(HaveOccurred())
			Expect(ret).To(Equal([]byte("https://otherurl")))
			ret, err = kvStore.Get(ctx, key2.ComposeKey())
			Expect(err).ToNot(HaveOccurred())
			Expect(ret).To(Equal([]byte("def456")))

			key.TemplateVersion = "mytv"
			key2.TemplateVersion = "mytv"
			ret, err = kvStore.Get(ctx, key.ComposeKey())
			Expect(err).ToNot(HaveOccurred())
			Expect(ret).To(BeEmpty())
			ret, err = kvStore.Get(ctx, key2.ComposeKey())
			Expect(err).ToNot(HaveOccurred())
			Expect(ret).To(BeEmpty())
		})
	})
})
