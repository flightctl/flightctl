package auxiliary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeKeycloak is a minimal stand-in for the Keycloak Admin REST API, enough to
// drive EnsureCatalogCollectorClient through both the fresh-import and the
// reused-container paths without running a container.
type fakeKeycloak struct {
	// clients is the realm's client list, keyed by clientId.
	clients map[string]*keycloakClientRep
	// mappers holds the protocol mappers of each client, keyed by client UUID.
	mappers map[string][]keycloakMapperRep
	// groups is the realm's group list.
	groups []keycloakGroupRep
	// groupJoins records "userID/groupID" memberships that were added.
	groupJoins []string
	// requests records every admin call as "METHOD path", in order.
	requests []string
	// tokenStatus lets a test make the admin token request fail.
	tokenStatus int

	nextID int
}

func newFakeKeycloak() *fakeKeycloak {
	return &fakeKeycloak{
		clients:     map[string]*keycloakClientRep{},
		mappers:     map[string][]keycloakMapperRep{},
		groups:      []keycloakGroupRep{{ID: "group-org-default", Name: "org-default", Path: "/org-default"}},
		tokenStatus: http.StatusOK,
	}
}

func (f *fakeKeycloak) newID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

// addClient seeds a client as a previous realm import would have left it.
func (f *fakeKeycloak) addClient(rep keycloakClientRep) *keycloakClientRep {
	stored := rep
	if stored.ID == "" {
		stored.ID = f.newID("client")
	}
	mappers := make([]keycloakMapperRep, 0, len(stored.ProtocolMappers))
	for _, mapper := range stored.ProtocolMappers {
		mapper.ID = f.newID("mapper")
		mappers = append(mappers, mapper)
	}
	f.mappers[stored.ID] = mappers
	stored.ProtocolMappers = nil
	f.clients[stored.ClientID] = &stored
	return &stored
}

func (f *fakeKeycloak) start(t *testing.T) *Keycloak {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(server.Close)
	return &Keycloak{URL: server.URL}
}

const fakeAdminPrefix = "/admin/realms/" + keycloakRealmName

func (f *fakeKeycloak) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		if f.tokenStatus != http.StatusOK {
			w.WriteHeader(f.tokenStatus)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		writeJSON(w, map[string]string{"access_token": "fake-admin-token"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, fakeAdminPrefix)
	f.requests = append(f.requests, r.Method+" "+path)

	switch {
	case strings.Contains(path, "/protocol-mappers/models"):
		f.handleMappers(w, r, path)
	case strings.HasSuffix(path, "/service-account-user"):
		f.handleServiceAccountUser(w, path)
	case strings.HasPrefix(path, "/clients"):
		f.handleClients(w, r, path)
	case strings.HasPrefix(path, "/groups"):
		f.handleGroups(w, r, path)
	case strings.HasPrefix(path, "/users/"):
		f.handleUserGroups(w, r, path)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeKeycloak) handleClients(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case r.Method == http.MethodGet && path == "/clients":
		found := []keycloakClientRep{}
		if client, ok := f.clients[r.URL.Query().Get("clientId")]; ok {
			found = append(found, *client)
		}
		writeJSON(w, found)

	case r.Method == http.MethodPost && path == "/clients":
		var rep keycloakClientRep
		decodeBody(w, r, &rep)
		f.addClient(rep)
		w.WriteHeader(http.StatusCreated)

	case r.Method == http.MethodPut:
		var rep keycloakClientRep
		decodeBody(w, r, &rep)
		existing, ok := f.clients[rep.ClientID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rep.ID = existing.ID
		// Keycloak ignores protocolMappers on a client update; the fake must
		// too, otherwise the test would not catch it if the code relied on it.
		rep.ProtocolMappers = nil
		f.clients[rep.ClientID] = &rep
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeKeycloak) handleMappers(w http.ResponseWriter, r *http.Request, path string) {
	uuid := clientUUIDFromPath(path)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, f.mappers[uuid])

	case http.MethodPost:
		var mapper keycloakMapperRep
		decodeBody(w, r, &mapper)
		mapper.ID = f.newID("mapper")
		f.mappers[uuid] = append(f.mappers[uuid], mapper)
		w.WriteHeader(http.StatusCreated)

	case http.MethodPut:
		var mapper keycloakMapperRep
		decodeBody(w, r, &mapper)
		for i := range f.mappers[uuid] {
			if f.mappers[uuid][i].ID == mapper.ID {
				f.mappers[uuid][i] = mapper
			}
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeKeycloak) handleServiceAccountUser(w http.ResponseWriter, path string) {
	uuid := clientUUIDFromPath(path)
	for _, client := range f.clients {
		if client.ID != uuid {
			continue
		}
		if !client.ServiceAccountsEnabled {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, keycloakUserRep{
			ID:       "user-" + client.ClientID,
			Username: "service-account-" + client.ClientID,
		})
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *fakeKeycloak) handleGroups(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case r.Method == http.MethodGet && path == "/groups":
		writeJSON(w, f.groups)

	case r.Method == http.MethodPost && path == "/groups":
		var group keycloakGroupRep
		decodeBody(w, r, &group)
		group.ID = f.newID("group")
		group.Path = "/" + group.Name
		f.groups = append(f.groups, group)
		w.WriteHeader(http.StatusCreated)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeKeycloak) handleUserGroups(w http.ResponseWriter, r *http.Request, path string) {
	userAndGroup := strings.Split(strings.TrimPrefix(path, "/users/"), "/groups/")
	if r.Method != http.MethodPut || len(userAndGroup) != 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.groupJoins = append(f.groupJoins, userAndGroup[0]+"/"+userAndGroup[1])
	w.WriteHeader(http.StatusNoContent)
}

// clientUUIDFromPath pulls the client UUID out of "/clients/<uuid>/...".
func clientUUIDFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/clients/"), "/")
	return parts[0]
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestEnsureCatalogCollectorClient(t *testing.T) {
	// otherClient stands for a client an earlier suite created; it must survive
	// untouched so reuse never costs another suite its configuration.
	otherClient := keycloakClientRep{ClientID: "flightctl-client", Name: "flightctl-client", Enabled: true}

	tests := []struct {
		name string
		// seed prepares the realm as the fixture would find it.
		seed func(f *fakeKeycloak)
		// wantCreated is true when the client had to be created.
		wantCreated bool
	}{
		{
			name: "When the realm was imported with the client it should reconcile it in place",
			seed: func(f *fakeKeycloak) {
				f.addClient(otherClient)
				f.addClient(catalogCollectorClient())
			},
		},
		{
			name: "When a reused realm has no collector client it should create it",
			seed: func(f *fakeKeycloak) {
				// Exactly the invalid_client situation: a Keycloak left behind
				// by a run that predates this suite.
				f.addClient(otherClient)
			},
			wantCreated: true,
		},
		{
			name: "When a reused realm has a stale collector client it should correct it",
			seed: func(f *fakeKeycloak) {
				f.addClient(otherClient)
				stale := catalogCollectorClient()
				stale.Secret = "an-old-secret"
				stale.ServiceAccountsEnabled = false
				stale.ProtocolMappers = nil
				f.addClient(stale)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeKeycloak()
			tt.seed(fake)
			keycloak := fake.start(t)

			require.NoError(t, keycloak.EnsureCatalogCollectorClient(context.Background()))

			stored, ok := fake.clients[KeycloakE2ECatalogCollectorClientID]
			require.True(t, ok, "the collector client must exist after reconciliation")
			require.Equal(t, KeycloakE2ECatalogCollectorSecret, stored.Secret,
				"the client secret must match the one the suite configures the collector with")
			require.True(t, stored.ServiceAccountsEnabled,
				"the client-credentials grant requires a service account")
			require.False(t, stored.PublicClient)
			require.True(t, stored.Enabled)

			require.Contains(t, fake.mappers, stored.ID)
			var audience *keycloakMapperRep
			for i := range fake.mappers[stored.ID] {
				if fake.mappers[stored.ID][i].Name == keycloakCatalogCollectorAudienceMapper {
					audience = &fake.mappers[stored.ID][i]
				}
			}
			require.NotNil(t, audience, "the access token needs the audience mapper")
			require.Equal(t, KeycloakE2ECatalogCollectorClientID, audience.Config["included.client.audience"])
			require.Equal(t, "true", audience.Config["access.token.claim"])

			require.Contains(t, fake.groupJoins,
				"user-"+KeycloakE2ECatalogCollectorClientID+"/group-org-default",
				"the service account must join the default organization group")

			preserved, ok := fake.clients[otherClient.ClientID]
			require.True(t, ok, "clients owned by other suites must not be removed")
			require.Equal(t, otherClient.Name, preserved.Name, "other clients must not be rewritten")

			created := false
			for _, request := range fake.requests {
				if request == "POST /clients" {
					created = true
				}
			}
			require.Equal(t, tt.wantCreated, created,
				"an existing client must be updated in place, a missing one created")
		})
	}
}

// TestEnsureCatalogCollectorClientIsIdempotent runs the reconciliation twice
// against the same realm, which is what a reused container sees on the second
// run of the suite.
func TestEnsureCatalogCollectorClientIsIdempotent(t *testing.T) {
	fake := newFakeKeycloak()
	keycloak := fake.start(t)

	require.NoError(t, keycloak.EnsureCatalogCollectorClient(context.Background()))
	firstPass := fake.clients[KeycloakE2ECatalogCollectorClientID]
	require.NotNil(t, firstPass)
	mappersAfterFirst := len(fake.mappers[firstPass.ID])

	require.NoError(t, keycloak.EnsureCatalogCollectorClient(context.Background()))

	secondPass := fake.clients[KeycloakE2ECatalogCollectorClientID]
	require.NotNil(t, secondPass)
	require.Equal(t, firstPass.ID, secondPass.ID, "the second pass must not replace the client")
	require.Len(t, fake.clients, 1, "the second pass must not add a duplicate client")
	require.Len(t, fake.mappers[secondPass.ID], mappersAfterFirst,
		"the second pass must not add a duplicate audience mapper")

	creates := 0
	for _, request := range fake.requests {
		if request == "POST /clients" {
			creates++
		}
	}
	require.Equal(t, 1, creates, "the client must be created once and reconciled thereafter")
}

func TestEnsureCatalogCollectorClientReportsAdminFailures(t *testing.T) {
	t.Run("When the admin token is refused it should return an error", func(t *testing.T) {
		fake := newFakeKeycloak()
		fake.tokenStatus = http.StatusUnauthorized
		keycloak := fake.start(t)

		err := keycloak.EnsureCatalogCollectorClient(context.Background())

		require.Error(t, err)
		require.Contains(t, err.Error(), "obtain Keycloak admin token")
	})

	t.Run("When Keycloak was never started it should return an error", func(t *testing.T) {
		keycloak := &Keycloak{}

		err := keycloak.EnsureCatalogCollectorClient(context.Background())

		require.Error(t, err)
		require.Contains(t, err.Error(), "not started")
	})
}
