package auxiliary

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const keycloakAudienceMapperType = "oidc-audience-mapper"

// TestKeycloakRealmClientAudienceMappers verifies Keycloak access tokens include the client audience expected by authprovider tests.
func TestKeycloakRealmClientAudienceMappers(t *testing.T) {
	tests := []struct {
		name     string
		clientID string
	}{
		{name: "When the OIDC client is imported it should include its client ID in access token audiences", clientID: "flightctl-client"},
		{name: "When the OAuth2 client is imported it should include its client ID in access token audiences", clientID: "flightctl-oauth2-client"},
	}

	realm := readKeycloakRealmFixture(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := findRealmClient(t, realm, tt.clientID)

			require.True(t, client.hasAccessTokenAudienceMapper(tt.clientID), "client %q must add its client ID to access token audiences", tt.clientID)
		})
	}
}

type keycloakRealmFixture struct {
	Clients []keycloakRealmClient `json:"clients"`
}

type keycloakRealmClient struct {
	ClientID        string                   `json:"clientId"`
	ProtocolMappers []keycloakProtocolMapper `json:"protocolMappers"`
}

type keycloakProtocolMapper struct {
	ProtocolMapper string            `json:"protocolMapper"`
	Config         map[string]string `json:"config"`
}

// readKeycloakRealmFixture parses the e2e Keycloak realm import fixture.
func readKeycloakRealmFixture(t *testing.T) keycloakRealmFixture {
	t.Helper()
	realmPath, err := getKeycloakRealmPath()
	require.NoError(t, err)

	realmJSON, err := os.ReadFile(realmPath)
	require.NoError(t, err)

	var realm keycloakRealmFixture
	require.NoError(t, json.Unmarshal(realmJSON, &realm))
	return realm
}

// findRealmClient returns the requested client from the parsed Keycloak realm fixture.
func findRealmClient(t *testing.T, realm keycloakRealmFixture, clientID string) keycloakRealmClient {
	t.Helper()
	for _, client := range realm.Clients {
		if client.ClientID == clientID {
			return client
		}
	}
	require.Failf(t, "missing Keycloak client", "client %q not found in realm fixture", clientID)
	return keycloakRealmClient{}
}

// hasAccessTokenAudienceMapper reports whether a client adds the expected audience to access tokens.
func (c keycloakRealmClient) hasAccessTokenAudienceMapper(audience string) bool {
	for _, mapper := range c.ProtocolMappers {
		if mapper.ProtocolMapper != keycloakAudienceMapperType {
			continue
		}
		if mapper.Config["included.client.audience"] != audience {
			continue
		}
		if mapper.Config["access.token.claim"] != "true" {
			continue
		}
		return true
	}
	return false
}
