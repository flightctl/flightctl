package updatercatalog_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/updatercatalog"
	"github.com/stretchr/testify/require"
)

func hash(s string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(s))) }
func candidate() updatercatalog.Candidate {
	return updatercatalog.Candidate{Revision: 1, InputDigest: hash("input"), Releases: []updatercatalog.Release{{Name: "orin", Version: "1", DescriptorDigest: hash(`{"code":"A"}`), Descriptor: []byte(`{"code":"A"}`), Schemas: []updatercatalog.Schema{{Digest: hash("config"), ObservationSchema: []byte(`{"type":"object"}`), BudgetKeys: []string{"maxCycles"}}}}}}
}
func TestCandidateValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*updatercatalog.Candidate)
	}{
		{"zero revision", func(c *updatercatalog.Candidate) { c.Revision = 0 }},
		{"invalid input digest", func(c *updatercatalog.Candidate) { c.InputDigest = "invalid" }},
		{"invalid descriptor digest", func(c *updatercatalog.Candidate) { c.Releases[0].DescriptorDigest = hash("different") }},
		{"invalid descriptor JSON", func(c *updatercatalog.Candidate) {
			c.Releases[0].Descriptor = []byte("invalid")
			c.Releases[0].DescriptorDigest = hash("invalid")
		}},
		{"duplicate release", func(c *updatercatalog.Candidate) { c.Releases = append(c.Releases, c.Releases[0]) }},
		{"missing evidence", func(c *updatercatalog.Candidate) { c.Releases[0].Schemas = nil }},
		{"invalid schema digest", func(c *updatercatalog.Candidate) { c.Releases[0].Schemas[0].Digest = "invalid" }},
		{"duplicate budget key", func(c *updatercatalog.Candidate) {
			c.Releases[0].Schemas[0].BudgetKeys = []string{"maxCycles", "maxCycles"}
		}},
		{"invalid revocation", func(c *updatercatalog.Candidate) {
			c.Revocations = []model.UpdaterRevocation{{Name: "unknown", Version: "1", DescriptorDigest: hash("x"), Reason: "", Revision: 2}}
		}},
		{"empty observation JSON", func(c *updatercatalog.Candidate) { c.Releases[0].Schemas[0].ObservationSchema = nil }},
	}
	for _, tc := range cases {
		t.Run("When "+tc.name+" it should reject before persistence", func(t *testing.T) { c := candidate(); tc.change(&c); require.Error(t, c.Validate()) })
	}
	t.Run("When metadata is valid it should accept", func(t *testing.T) { c := candidate(); require.NoError(t, c.Validate()) })
	t.Run("When the database is missing it should reject construction", func(t *testing.T) { _, err := updatercatalog.NewStore(nil); require.Error(t, err) })
}
