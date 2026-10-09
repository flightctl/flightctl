package auxiliary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// The shared e2e-keycloak container is reused by name across runs, and a realm
// is only imported when Keycloak starts with an empty database. Editing
// flightctl-realm.json therefore has no effect on an instance a previous run
// left behind: a developer who ran an older suite would reuse a Keycloak whose
// flightctl realm has no catalog collector client, and the collector's
// client-credentials grant would fail with invalid_client.
//
// The realm file stays the declarative source for a fresh instance; the Admin
// REST API below reconciles the same objects on every start so a reused
// instance converges to the identical configuration. Only the objects this
// suite owns are touched, so clients and users created by other suites (or by
// hand) survive untouched.
const (
	// keycloakAdminUsername and keycloakAdminPassword are the bootstrap
	// credentials Start passes to the container.
	keycloakAdminUsername = "admin"
	keycloakAdminPassword = "admin" //nolint:gosec // G101: e2e bootstrap admin password only
	// keycloakAdminCLIClientID is the built-in master-realm client that serves
	// the direct access grant used to obtain an admin token.
	keycloakAdminCLIClientID = "admin-cli"

	// keycloakCatalogCollectorAudienceMapper is the audience mapper name, kept
	// identical to the one in flightctl-realm.json so a fresh import and a
	// reconciled reuse produce the same object.
	keycloakCatalogCollectorAudienceMapper = "flightctl-catalog-collector-audience"
	// keycloakOrgDefaultGroupName is the realm group every e2e identity joins,
	// matching the "groups" entry in flightctl-realm.json.
	keycloakOrgDefaultGroupName = "org-default"

	keycloakAdminRequestTimeout = 30 * time.Second
)

// keycloakClientRep is the subset of Keycloak's client representation this
// fixture manages. Fields are pointers only where Keycloak distinguishes unset
// from false; everything here is always sent.
type keycloakClientRep struct {
	ID                        string              `json:"id,omitempty"`
	ClientID                  string              `json:"clientId"`
	Name                      string              `json:"name"`
	Description               string              `json:"description"`
	Enabled                   bool                `json:"enabled"`
	PublicClient              bool                `json:"publicClient"`
	Secret                    string              `json:"secret"`
	Protocol                  string              `json:"protocol"`
	StandardFlowEnabled       bool                `json:"standardFlowEnabled"`
	DirectAccessGrantsEnabled bool                `json:"directAccessGrantsEnabled"`
	ServiceAccountsEnabled    bool                `json:"serviceAccountsEnabled"`
	ProtocolMappers           []keycloakMapperRep `json:"protocolMappers,omitempty"`
}

// keycloakMapperRep is a protocol mapper representation.
type keycloakMapperRep struct {
	ID              string            `json:"id,omitempty"`
	Name            string            `json:"name"`
	Protocol        string            `json:"protocol"`
	ProtocolMapper  string            `json:"protocolMapper"`
	ConsentRequired bool              `json:"consentRequired"`
	Config          map[string]string `json:"config"`
}

// keycloakGroupRep is the subset of a group representation used for lookups.
type keycloakGroupRep struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// keycloakUserRep is the subset of a user representation used for lookups.
type keycloakUserRep struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// catalogCollectorClient is the desired state of the client the catalog
// collector e2e suite authenticates with. It mirrors the entry in
// flightctl-realm.json; the two must stay in step.
func catalogCollectorClient() keycloakClientRep {
	return keycloakClientRep{
		ClientID:                  KeycloakE2ECatalogCollectorClientID,
		Name:                      KeycloakE2ECatalogCollectorClientID,
		Description:               "Confidential client used by the catalog collector e2e suite to obtain tokens with the OAuth2 client-credentials grant.",
		Enabled:                   true,
		PublicClient:              false,
		Secret:                    KeycloakE2ECatalogCollectorSecret,
		Protocol:                  "openid-connect",
		StandardFlowEnabled:       false,
		DirectAccessGrantsEnabled: false,
		ServiceAccountsEnabled:    true,
		ProtocolMappers:           []keycloakMapperRep{catalogCollectorAudienceMapper()},
	}
}

// catalogCollectorAudienceMapper puts the client's own id in the access token
// audience, which is what the Flight Control API validates the token against.
func catalogCollectorAudienceMapper() keycloakMapperRep {
	return keycloakMapperRep{
		Name:           keycloakCatalogCollectorAudienceMapper,
		Protocol:       "openid-connect",
		ProtocolMapper: "oidc-audience-mapper",
		Config: map[string]string{
			"included.client.audience": KeycloakE2ECatalogCollectorClientID,
			"id.token.claim":           "false",
			"access.token.claim":       "true",
			"userinfo.token.claim":     "false",
		},
	}
}

// EnsureCatalogCollectorClient reconciles the catalog collector client, its
// audience mapper, and its service account against the running realm.
//
// It is idempotent and safe on both a freshly imported realm (where it finds
// everything already in place and only verifies it) and a realm left behind by
// an earlier run that predates the client (where it creates it). Clients and
// users this fixture does not own are never read back or rewritten.
func (k *Keycloak) EnsureCatalogCollectorClient(ctx context.Context) error {
	token, err := k.adminToken(ctx)
	if err != nil {
		return fmt.Errorf("obtain Keycloak admin token: %w", err)
	}

	desired := catalogCollectorClient()
	clientUUID, err := k.ensureClient(ctx, token, desired)
	if err != nil {
		return err
	}
	if err := k.ensureAudienceMapper(ctx, token, clientUUID); err != nil {
		return err
	}
	if err := k.ensureServiceAccount(ctx, token, clientUUID); err != nil {
		return err
	}
	logrus.Infof("Keycloak: client %q is present in realm %q with a service account and audience mapper",
		desired.ClientID, keycloakRealmName)
	return nil
}

// adminToken fetches an access token for the master realm admin.
func (k *Keycloak) adminToken(ctx context.Context) (string, error) {
	if k.URL == "" {
		return "", fmt.Errorf("keycloak is not started")
	}
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {keycloakAdminCLIClientID},
		"username":   {keycloakAdminUsername},
		"password":   {keycloakAdminPassword},
	}
	endpoint := k.URL + "/realms/master/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build admin token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: keycloakAdminRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request admin token: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close error is not actionable here

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read admin token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("admin token request returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode admin token response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("admin token response contained no access_token")
	}
	return payload.AccessToken, nil
}

// ensureClient creates the client when it is absent and otherwise updates the
// existing one in place, returning the client's internal UUID.
func (k *Keycloak) ensureClient(ctx context.Context, token string, desired keycloakClientRep) (string, error) {
	var existing []keycloakClientRep
	query := "/clients?clientId=" + url.QueryEscape(desired.ClientID)
	if err := k.adminDo(ctx, token, http.MethodGet, query, nil, &existing); err != nil {
		return "", fmt.Errorf("look up client %q: %w", desired.ClientID, err)
	}

	if len(existing) == 0 {
		// Create carries the protocol mappers; Keycloak also provisions the
		// service account user because serviceAccountsEnabled is set.
		if err := k.adminDo(ctx, token, http.MethodPost, "/clients", desired, nil); err != nil {
			return "", fmt.Errorf("create client %q: %w", desired.ClientID, err)
		}
		if err := k.adminDo(ctx, token, http.MethodGet, query, nil, &existing); err != nil {
			return "", fmt.Errorf("re-read created client %q: %w", desired.ClientID, err)
		}
		if len(existing) == 0 {
			return "", fmt.Errorf("client %q is still absent after creating it", desired.ClientID)
		}
		logrus.Infof("Keycloak: created client %q in realm %q", desired.ClientID, keycloakRealmName)
		return existing[0].ID, nil
	}

	// A reused realm may hold a client from an older revision of this fixture
	// (wrong secret, service accounts off). Rewrite the fields this fixture
	// owns and keep the existing id so the update targets the same object.
	update := desired
	update.ID = existing[0].ID
	// Mappers are managed through their own endpoint: Keycloak ignores
	// protocolMappers on a client update.
	update.ProtocolMappers = nil
	if err := k.adminDo(ctx, token, http.MethodPut, "/clients/"+existing[0].ID, update, nil); err != nil {
		return "", fmt.Errorf("update client %q: %w", desired.ClientID, err)
	}
	return existing[0].ID, nil
}

// ensureAudienceMapper adds the audience mapper when missing and corrects its
// configuration when it drifted.
func (k *Keycloak) ensureAudienceMapper(ctx context.Context, token, clientUUID string) error {
	var mappers []keycloakMapperRep
	path := "/clients/" + clientUUID + "/protocol-mappers/models"
	if err := k.adminDo(ctx, token, http.MethodGet, path, nil, &mappers); err != nil {
		return fmt.Errorf("list protocol mappers of client %q: %w", KeycloakE2ECatalogCollectorClientID, err)
	}

	desired := catalogCollectorAudienceMapper()
	for _, mapper := range mappers {
		if mapper.Name != desired.Name {
			continue
		}
		if mapperConfigMatches(mapper.Config, desired.Config) {
			return nil
		}
		update := desired
		update.ID = mapper.ID
		if err := k.adminDo(ctx, token, http.MethodPut, path+"/"+mapper.ID, update, nil); err != nil {
			return fmt.Errorf("update audience mapper %q: %w", desired.Name, err)
		}
		logrus.Infof("Keycloak: corrected audience mapper %q", desired.Name)
		return nil
	}

	if err := k.adminDo(ctx, token, http.MethodPost, path, desired, nil); err != nil {
		return fmt.Errorf("create audience mapper %q: %w", desired.Name, err)
	}
	logrus.Infof("Keycloak: created audience mapper %q", desired.Name)
	return nil
}

// mapperConfigMatches reports whether every key this fixture manages already
// holds the desired value. Keys Keycloak adds on its own are ignored, so an
// otherwise-correct mapper is not rewritten on every run.
func mapperConfigMatches(actual, desired map[string]string) bool {
	for key, value := range desired {
		if actual[key] != value {
			return false
		}
	}
	return true
}

// ensureServiceAccount verifies the client's service account user exists and
// joins it to the default organization group, matching what the realm import
// does for a fresh instance.
func (k *Keycloak) ensureServiceAccount(ctx context.Context, token, clientUUID string) error {
	var user keycloakUserRep
	path := "/clients/" + clientUUID + "/service-account-user"
	if err := k.adminDo(ctx, token, http.MethodGet, path, nil, &user); err != nil {
		return fmt.Errorf("read the service account user of client %q: %w", KeycloakE2ECatalogCollectorClientID, err)
	}
	if user.ID == "" {
		return fmt.Errorf("client %q has no service account user; serviceAccountsEnabled did not take effect",
			KeycloakE2ECatalogCollectorClientID)
	}

	group, err := k.ensureOrgDefaultGroup(ctx, token)
	if err != nil {
		return err
	}
	// Joining a group the user is already in is a no-op for Keycloak, so this
	// needs no membership read first.
	if err := k.adminDo(ctx, token, http.MethodPut, "/users/"+user.ID+"/groups/"+group.ID, nil, nil); err != nil {
		return fmt.Errorf("add service account %q to group %q: %w", user.Username, group.Path, err)
	}
	return nil
}

// ensureOrgDefaultGroup returns the default organization group, creating it if
// a reused realm predates it.
func (k *Keycloak) ensureOrgDefaultGroup(ctx context.Context, token string) (keycloakGroupRep, error) {
	var groups []keycloakGroupRep
	query := "/groups?search=" + url.QueryEscape(keycloakOrgDefaultGroupName)
	if err := k.adminDo(ctx, token, http.MethodGet, query, nil, &groups); err != nil {
		return keycloakGroupRep{}, fmt.Errorf("look up group %q: %w", keycloakOrgDefaultGroupName, err)
	}
	for _, group := range groups {
		if group.Name == keycloakOrgDefaultGroupName {
			return group, nil
		}
	}

	if err := k.adminDo(ctx, token, http.MethodPost, "/groups",
		keycloakGroupRep{Name: keycloakOrgDefaultGroupName}, nil); err != nil {
		return keycloakGroupRep{}, fmt.Errorf("create group %q: %w", keycloakOrgDefaultGroupName, err)
	}
	if err := k.adminDo(ctx, token, http.MethodGet, query, nil, &groups); err != nil {
		return keycloakGroupRep{}, fmt.Errorf("re-read created group %q: %w", keycloakOrgDefaultGroupName, err)
	}
	for _, group := range groups {
		if group.Name == keycloakOrgDefaultGroupName {
			return group, nil
		}
	}
	return keycloakGroupRep{}, fmt.Errorf("group %q is still absent after creating it", keycloakOrgDefaultGroupName)
}

// adminDo performs one bounded Admin REST call against the flightctl realm.
// path is relative to /admin/realms/<realm>. out may be nil.
func (k *Keycloak) adminDo(ctx context.Context, token, method, path string, payload any, out any) error {
	if k.URL == "" {
		return fmt.Errorf("keycloak is not started")
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	endpoint := k.URL + "/admin/realms/" + keycloakRealmName + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: keycloakAdminRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close error is not actionable here

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: unexpected status %s: %s", method, path, resp.Status, strings.TrimSpace(string(responseBody)))
	}
	if out == nil {
		return nil
	}
	// 204 and empty bodies are normal for writes; nothing to decode.
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}
