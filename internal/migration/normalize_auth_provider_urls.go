package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	authprovider "github.com/flightctl/flightctl/internal/auth/provider"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const normalizeAuthProviderURLsMigrationKey = "normalize_auth_provider_urls_v1"

type oidcUniquenessKey struct {
	issuer   string
	clientID string
}

type oauth2UniquenessKey struct {
	userinfoURL string
	clientID    string
}

// normalizeAuthProviderURLs rewrites stored OIDC/OAuth2 auth provider URL fields to the
// canonical form (lowercase + trailing-slash strip) so unique indexes and runtime lookups
// match write-path normalization. Runs once via schema_migrations.
//
// If two existing rows would collide after normalization (e.g. issuer with/without a
// trailing slash, or differing only by letter case, for the same clientId), the migration
// fails so an admin can resolve the duplicate before upgrade completes.
func normalizeAuthProviderURLs(ctx context.Context, tx *gorm.DB) error {
	return tx.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.SchemaMigration{Key: normalizeAuthProviderURLsMigrationKey, AppliedAt: time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		var rows []model.AuthProvider
		if err := tx.Find(&rows).Error; err != nil {
			return fmt.Errorf("list auth providers for URL normalization: %w", err)
		}

		seenOIDC := make(map[oidcUniquenessKey]string)
		seenOAuth2 := make(map[oauth2UniquenessKey]string)

		for i := range rows {
			row := &rows[i]
			if row.Spec == nil {
				continue
			}
			before := row.Spec.Data
			after, err := cloneAuthProviderSpec(before)
			if err != nil {
				return fmt.Errorf("clone auth provider %q for URL normalization: %w", row.Name, err)
			}
			if err := authprovider.NormalizeAuthProviderSpecURLs(&after); err != nil {
				return fmt.Errorf("normalize auth provider %q (org %s): %w", row.Name, row.OrgID, err)
			}

			if err := recordNormalizedAuthProviderKey(row.Name, after, seenOIDC, seenOAuth2); err != nil {
				return err
			}

			if reflect.DeepEqual(before, after) {
				continue
			}
			if err := tx.Model(&model.AuthProvider{}).
				Where("org_id = ? AND name = ?", row.OrgID, row.Name).
				Update("spec", model.MakeJSONField(after)).Error; err != nil {
				return fmt.Errorf("update auth provider %q after URL normalization: %w", row.Name, err)
			}
		}

		return nil
	})
}

func recordNormalizedAuthProviderKey(
	name string,
	spec domain.AuthProviderSpec,
	seenOIDC map[oidcUniquenessKey]string,
	seenOAuth2 map[oauth2UniquenessKey]string,
) error {
	discriminator, err := spec.Discriminator()
	if err != nil {
		return nil
	}
	switch discriminator {
	case string(api.Oidc):
		oidcSpec, err := spec.AsOIDCProviderSpec()
		if err != nil {
			return fmt.Errorf("parse OIDC auth provider %q after normalize: %w", name, err)
		}
		key := oidcUniquenessKey{issuer: oidcSpec.Issuer, clientID: oidcSpec.ClientId}
		if existing, ok := seenOIDC[key]; ok {
			return fmt.Errorf("cannot normalize auth provider URLs: OIDC providers %q and %q would share issuer %q and clientId %q after URL normalization; resolve the duplicate and re-run migration", existing, name, key.issuer, key.clientID)
		}
		seenOIDC[key] = name
	case string(api.Oauth2):
		oauth2Spec, err := spec.AsOAuth2ProviderSpec()
		if err != nil {
			return fmt.Errorf("parse OAuth2 auth provider %q after normalize: %w", name, err)
		}
		key := oauth2UniquenessKey{userinfoURL: oauth2Spec.UserinfoUrl, clientID: oauth2Spec.ClientId}
		if existing, ok := seenOAuth2[key]; ok {
			return fmt.Errorf("cannot normalize auth provider URLs: OAuth2 providers %q and %q would share userinfoUrl %q and clientId %q after URL normalization; resolve the duplicate and re-run migration", existing, name, key.userinfoURL, key.clientID)
		}
		seenOAuth2[key] = name
	}
	return nil
}

func cloneAuthProviderSpec(spec domain.AuthProviderSpec) (domain.AuthProviderSpec, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return domain.AuthProviderSpec{}, err
	}
	var cloned domain.AuthProviderSpec
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return domain.AuthProviderSpec{}, err
	}
	return cloned, nil
}
