package cookies

import (
	b64 "encoding/base64"
	"encoding/json"
	"net/http"
)

const CookieSessionName = "flightctl-session"

type TokenData struct {
	// Token is the authentication token to use for API calls
	//   - OIDC/K8s: IDToken (JWT)
	//   - OAuth2/AAP/OpenShift: AccessToken (opaque)
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	Provider     string `json:"provider,omitempty"`
}

func EncodeTokenForCookie(value TokenData) (string, error) {
	cookieVal, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return b64.StdEncoding.EncodeToString(cookieVal), nil
}

func ParseSessionCookie(r *http.Request) (TokenData, error) {
	tokenData := TokenData{}
	cookie, err := r.Cookie(CookieSessionName)
	if err != nil {
		return tokenData, err
	}

	val, err := b64.StdEncoding.DecodeString(cookie.Value)
	if err != nil {
		return tokenData, err
	}

	err = json.Unmarshal(val, &tokenData)
	return tokenData, err
}
