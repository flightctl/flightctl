package util

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/redhatinsights/platform-go-middlewares/identity"
	"github.com/sirupsen/logrus"
)

func ParseXRHIdentityHeader(identityHeader string) (*identity.XRHID, error) {
	var XRHIdentity identity.XRHID
	decodedIdentity, err := base64.StdEncoding.DecodeString(identityHeader)
	if err != nil {
		return nil, fmt.Errorf("error decoding Identity: %v", err)
	}

	err = json.Unmarshal(decodedIdentity, &XRHIdentity)

	if err != nil {
		logrus.Errorf("x-rh-identity header is not a valid json: %s. Identity: %s", err.Error(), identityHeader)
		logrus.Errorf("x-rh-identity header is not valid json: %s. Identity: %s", err.Error(), identityHeader)
		return nil, fmt.Errorf("x-rh-identity header is not valid json: %w", err)
	}

	// XRHIdentity.Identity.User.UserID
	return &XRHIdentity, nil
}

type DecodedToken struct {
	UserId        string `json:"user_id"`
	OrgId         string `json:"org_id"`
	AccountNumber string `json:"account_number"`
	Username      string `json:"username"`
}

// Parse JWT token without decode key
func ParseJWTToken(tokenString string) (DecodedToken, error) {
	str := tokenString
	str = strings.Split(str, ".")[1]
	str = strings.ReplaceAll(str, "-/", "+")
	str = strings.ReplaceAll(str, "_/", "/")

	switch len(str) % 4 {
	case 0:
		break
	case 2:
		str += "=="
	case 3:
		str += "="
	default:
		return DecodedToken{}, errors.New("invalid token")
	}

	str = str + strings.Repeat("=", len(str)%4)
	str = strings.ReplaceAll(str, "-", "+")
	str = strings.ReplaceAll(str, "_", "/")

	data, err := base64.URLEncoding.DecodeString(str)
	if err != nil {
		return DecodedToken{}, err
	}

	str = string(data)
	str, err = url.QueryUnescape(str)
	if err != nil {
		return DecodedToken{}, err
	}

	var res DecodedToken
	err = json.Unmarshal([]byte(str), &res)
	fmt.Println(res)
	if err != nil {
		return DecodedToken{}, err
	}

	return res, nil
}
