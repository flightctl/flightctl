package store_test

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

func addEncryptionKey(name string) *encryption.V1Strategy {
	GinkgoHelper()
	mgr := encryption.GlobalManager()
	Expect(mgr).ToNot(BeNil())
	strategy, ok := mgr.GetStrategy("v1")
	Expect(ok).To(BeTrue())
	v1 := strategy.(*encryption.V1Strategy)
	key := make([]byte, 32)
	_, err := rand.Read(key)
	Expect(err).ToNot(HaveOccurred())
	Expect(v1.AddKey(name, key, false)).To(Succeed())
	return v1
}

func readStoredRepoPassword(db *gorm.DB, ctx context.Context, orgId uuid.UUID, name string) string {
	GinkgoHelper()
	var stored model.Repository
	Expect(db.WithContext(ctx).First(&stored, "org_id = ? AND name = ?", orgId, name).Error).To(Succeed())
	specJSON, err := json.Marshal(stored.Spec.Data)
	Expect(err).ToNot(HaveOccurred())

	var specMap map[string]any
	Expect(json.Unmarshal(specJSON, &specMap)).To(Succeed())
	httpConfig, ok := specMap["httpConfig"].(map[string]any)
	Expect(ok).To(BeTrue(), "expected httpConfig in spec for repo %s", name)
	password, ok := httpConfig["password"].(string)
	Expect(ok).To(BeTrue(), "expected password string in httpConfig for repo %s", name)
	return password
}
