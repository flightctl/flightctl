package authn

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	pkgcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// JWTGenerator handles JWT token generation for Linux authentication
type JWTGenerator struct {
	privateKey crypto.PrivateKey
	keyID      string
}

// NewJWTGenerator creates a new JWT generator using the existing CA key
func NewJWTGenerator(caClient *fccrypto.CAClient) (*JWTGenerator, error) {
	// Get the CA's private key from the existing CA client
	caConfig := caClient.Config()
	if caConfig == nil || caConfig.InternalConfig == nil {
		return nil, fmt.Errorf("CA configuration not available")
	}

	// Load the CA private key
	caKeyFile := fccrypto.CertStorePath(caConfig.InternalConfig.KeyFile, caConfig.InternalConfig.CertStore)
	privateKey, err := pkgcrypto.LoadKey(caKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load CA private key from %s: %w", caKeyFile, err)
	}

	// Generate a key ID based on the CA certificate
	caCertFile := fccrypto.CertStorePath(caConfig.InternalConfig.CertFile, caConfig.InternalConfig.CertStore)
	keyID, err := generateKeyIDFromCert(caCertFile)
	if err != nil {
		// Fallback to random key ID if we can't read the cert
		keyIDBytes := make([]byte, 8)
		if _, err := rand.Read(keyIDBytes); err != nil {
			return nil, fmt.Errorf("failed to generate key ID: %w", err)
		}
		keyID = fmt.Sprintf("%x", keyIDBytes)
	}

	return &JWTGenerator{
		privateKey: privateKey,
		keyID:      keyID,
	}, nil
}

// generateKeyIDFromCert generates a key ID from the CA certificate
func generateKeyIDFromCert(certFile string) (string, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return "", fmt.Errorf("failed to read certificate file: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Use the first 8 bytes of the certificate's serial number as key ID
	serialBytes := cert.SerialNumber.Bytes()
	if len(serialBytes) >= 8 {
		return fmt.Sprintf("%x", serialBytes[:8]), nil
	}

	// If serial is shorter, pad with zeros
	padded := make([]byte, 8)
	copy(padded, serialBytes)
	return fmt.Sprintf("%x", padded), nil
}

// GenerateToken creates a JWT token for the given identity
func (g *JWTGenerator) GenerateToken(identity common.Identity, expiration time.Duration) (string, error) {
	now := time.Now()

	// Create JWT token
	token := jwt.New()

	// Set standard claims
	if err := token.Set(jwt.SubjectKey, identity.GetUID()); err != nil {
		return "", fmt.Errorf("failed to set subject: %w", err)
	}
	if err := token.Set(jwt.IssuedAtKey, now.Unix()); err != nil {
		return "", fmt.Errorf("failed to set issued at: %w", err)
	}
	if err := token.Set(jwt.ExpirationKey, now.Add(expiration).Unix()); err != nil {
		return "", fmt.Errorf("failed to set expiration: %w", err)
	}
	if err := token.Set(jwt.NotBeforeKey, now.Unix()); err != nil {
		return "", fmt.Errorf("failed to set not before: %w", err)
	}

	// Set custom claims
	if err := token.Set("preferred_username", identity.GetUsername()); err != nil {
		return "", fmt.Errorf("failed to set preferred_username: %w", err)
	}
	if err := token.Set("roles", identity.GetRoles()); err != nil {
		return "", fmt.Errorf("failed to set roles: %w", err)
	}
	if err := token.Set("organizations", identity.GetOrganizations()); err != nil {
		return "", fmt.Errorf("failed to set organizations: %w", err)
	}

	// Create JWK from private key
	jwkKey, err := jwk.FromRaw(g.privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to create JWK: %w", err)
	}

	// Set key properties
	if err := jwkKey.Set(jwk.KeyIDKey, g.keyID); err != nil {
		return "", fmt.Errorf("failed to set key ID: %w", err)
	}

	// Determine the signing algorithm based on key type
	var signingAlg jwa.SignatureAlgorithm
	switch g.privateKey.(type) {
	case *rsa.PrivateKey:
		signingAlg = jwa.RS256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
			return "", fmt.Errorf("failed to set algorithm: %w", err)
		}
	default:
		// For ECDSA keys, use ES256
		signingAlg = jwa.ES256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return "", fmt.Errorf("failed to set algorithm: %w", err)
		}
	}

	// Sign the token
	tokenBytes, err := jwt.Sign(token, jwt.WithKey(signingAlg, jwkKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return string(tokenBytes), nil
}

type TokenGenerationRequest struct {
	Username      string
	UID           string
	Organizations []string
	Roles         []string
}

// GenerateTokenWithType creates a JWT token for the given identity with a specific token type
func (g *JWTGenerator) GenerateTokenWithType(request TokenGenerationRequest, expiration time.Duration, tokenType string) (string, error) {
	now := time.Now()

	// Create JWT token
	token := jwt.New()

	// Set standard claims
	if err := token.Set(jwt.SubjectKey, request.UID); err != nil {
		return "", fmt.Errorf("failed to set subject: %w", err)
	}
	if err := token.Set(jwt.IssuedAtKey, now.Unix()); err != nil {
		return "", fmt.Errorf("failed to set issued at: %w", err)
	}
	if err := token.Set(jwt.ExpirationKey, now.Add(expiration).Unix()); err != nil {
		return "", fmt.Errorf("failed to set expiration: %w", err)
	}
	if err := token.Set(jwt.NotBeforeKey, now.Unix()); err != nil {
		return "", fmt.Errorf("failed to set not before: %w", err)
	}

	// Set custom claims
	if err := token.Set("preferred_username", request.Username); err != nil {
		return "", fmt.Errorf("failed to set preferred_username: %w", err)
	}
	if err := token.Set("roles", request.Roles); err != nil {
		return "", fmt.Errorf("failed to set roles: %w", err)
	}

	if err := token.Set("organizations", request.Organizations); err != nil {
		return "", fmt.Errorf("failed to set organizations: %w", err)
	}

	// Set token type claim
	if err := token.Set("token_type", tokenType); err != nil {
		return "", fmt.Errorf("failed to set token_type: %w", err)
	}

	// Create JWK from private key
	jwkKey, err := jwk.FromRaw(g.privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to create JWK: %w", err)
	}

	// Set key properties
	if err := jwkKey.Set(jwk.KeyIDKey, g.keyID); err != nil {
		return "", fmt.Errorf("failed to set key ID: %w", err)
	}

	// Determine the signing algorithm based on key type
	var signingAlg jwa.SignatureAlgorithm
	switch g.privateKey.(type) {
	case *rsa.PrivateKey:
		signingAlg = jwa.RS256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
			return "", fmt.Errorf("failed to set algorithm: %w", err)
		}
	default:
		// For ECDSA keys, use ES256
		signingAlg = jwa.ES256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return "", fmt.Errorf("failed to set algorithm: %w", err)
		}
	}

	// Sign the token
	tokenBytes, err := jwt.Sign(token, jwt.WithKey(signingAlg, jwkKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return string(tokenBytes), nil
}

// ValidateTokenWithType validates a JWT token and ensures it has the correct token type
func (g *JWTGenerator) ValidateTokenWithType(tokenString string, expectedTokenType string) (*JWTIdentity, error) {
	// First validate the token normally
	identity, err := g.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}

	// Check if the token has the expected type
	if identity.parsedToken != nil {
		if tokenType, exists := identity.parsedToken.Get("token_type"); exists {
			if actualType, ok := tokenType.(string); ok && actualType == expectedTokenType {
				return identity, nil
			}
		}
	}

	return nil, fmt.Errorf("token type mismatch: expected %s", expectedTokenType)
}

// GetPublicKeyPEM returns the public key in PEM format for JWKS endpoint
func (g *JWTGenerator) GetPublicKeyPEM() (string, error) {
	// Get the public key from the private key
	signer, ok := g.privateKey.(crypto.Signer)
	if !ok {
		return "", fmt.Errorf("private key does not implement crypto.Signer")
	}

	publicKey := signer.Public()
	publicKeyBytes, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal public key: %w", err)
	}

	publicKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	})

	return string(publicKeyPEM), nil
}

// GetJWKS returns the JWKS (JSON Web Key Set) for this generator
func (g *JWTGenerator) GetJWKS() (map[string]interface{}, error) {
	// Get the public key from the private key
	signer, ok := g.privateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("private key does not implement crypto.Signer")
	}

	publicKey := signer.Public()

	// Create JWK from public key
	jwkKey, err := jwk.FromRaw(publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWK from public key: %w", err)
	}

	// Set key properties
	if err := jwkKey.Set(jwk.KeyIDKey, g.keyID); err != nil {
		return nil, fmt.Errorf("failed to set key ID: %w", err)
	}

	// Set algorithm and key type based on the key type
	switch g.privateKey.(type) {
	case *rsa.PrivateKey:
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
		if err := jwkKey.Set(jwk.KeyTypeKey, "RSA"); err != nil {
			return nil, fmt.Errorf("failed to set key type: %w", err)
		}
	default:
		// For ECDSA keys
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
		if err := jwkKey.Set(jwk.KeyTypeKey, "EC"); err != nil {
			return nil, fmt.Errorf("failed to set key type: %w", err)
		}
	}

	if err := jwkKey.Set(jwk.KeyUsageKey, "sig"); err != nil {
		return nil, fmt.Errorf("failed to set key usage: %w", err)
	}

	// Create JWKS
	jwks := map[string]interface{}{
		"keys": []interface{}{jwkKey},
	}

	return jwks, nil
}

// ValidateToken validates a JWT token using the generator's public key
func (g *JWTGenerator) ValidateToken(tokenString string) (*JWTIdentity, error) {
	// Get the public key from the private key
	signer, ok := g.privateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("private key does not implement crypto.Signer")
	}

	publicKey := signer.Public()

	// Create JWK from public key
	jwkKey, err := jwk.FromRaw(publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWK from public key: %w", err)
	}

	// Set key properties
	if err := jwkKey.Set(jwk.KeyIDKey, g.keyID); err != nil {
		return nil, fmt.Errorf("failed to set key ID: %w", err)
	}

	// Determine the signing algorithm based on key type
	var signingAlg jwa.SignatureAlgorithm
	switch g.privateKey.(type) {
	case *rsa.PrivateKey:
		signingAlg = jwa.RS256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
	default:
		// For ECDSA keys, use ES256
		signingAlg = jwa.ES256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
	}

	// Parse and validate token
	parsedToken, err := jwt.Parse([]byte(tokenString), jwt.WithKey(signingAlg, jwkKey), jwt.WithValidate(true))
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT token: %w", err)
	}

	// Create JWTIdentity
	identity := &JWTIdentity{}
	identity.parsedToken = parsedToken

	// Extract claims
	if sub, exists := parsedToken.Get("sub"); exists {
		if uid, ok := sub.(string); ok {
			identity.SetUID(uid)
		}
	}

	if preferredUsername, exists := parsedToken.Get("preferred_username"); exists {
		if username, ok := preferredUsername.(string); ok {
			identity.SetUsername(username)
		}
	}

	if roles, exists := parsedToken.Get("roles"); exists {
		if rolesList, ok := roles.([]interface{}); ok {
			roleStrings := make([]string, 0, len(rolesList))
			for _, role := range rolesList {
				if roleStr, ok := role.(string); ok {
					roleStrings = append(roleStrings, roleStr)
				}
			}
			identity.SetRoles(roleStrings)
		}
	}

	if orgs, exists := parsedToken.Get("organizations"); exists {
		if orgList, ok := orgs.([]interface{}); ok {
			orgStrings := make([]common.ReportedOrganization, 0, len(orgList))
			for _, org := range orgList {
				if orgStr, ok := org.(string); ok {
					orgStrings = append(orgStrings, common.ReportedOrganization{
						Name:         orgStr,
						IsInternalID: false,
						ID:           orgStr,
					})
				}
			}
			identity.SetOrganizations(orgStrings)
		}
	}

	return identity, nil
}
