package authn

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1alpha1/pam-issuer"
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
	case *ecdsa.PrivateKey:
		signingAlg = jwa.ES256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return "", fmt.Errorf("failed to set algorithm: %w", err)
		}
	case ed25519.PrivateKey:
		return "", fmt.Errorf("unsupported key type: Ed25519 keys are not currently supported")
	default:
		return "", fmt.Errorf("unsupported key type: %T", g.privateKey)
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
	Audience      []string // JWT audience claim (aud)
	Issuer        string   // JWT issuer claim (iss)
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
	// Set audience claim if provided
	if len(request.Audience) > 0 {
		if err := token.Set(jwt.AudienceKey, request.Audience); err != nil {
			return "", fmt.Errorf("failed to set audience: %w", err)
		}
	}
	// Set issuer claim if provided
	if request.Issuer != "" {
		if err := token.Set(jwt.IssuerKey, request.Issuer); err != nil {
			return "", fmt.Errorf("failed to set issuer: %w", err)
		}
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
	case *ecdsa.PrivateKey:
		signingAlg = jwa.ES256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return "", fmt.Errorf("failed to set algorithm: %w", err)
		}
	case ed25519.PrivateKey:
		return "", fmt.Errorf("unsupported key type: Ed25519 keys are not currently supported")
	default:
		return "", fmt.Errorf("unsupported key type: %T", g.privateKey)
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
func (g *JWTGenerator) GetJWKS() (*pamapi.JWKSResponse, error) {
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
	var alg string
	var kty string
	switch g.privateKey.(type) {
	case *rsa.PrivateKey:
		alg = "RS256"
		kty = "RSA"
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
		if err := jwkKey.Set(jwk.KeyTypeKey, "RSA"); err != nil {
			return nil, fmt.Errorf("failed to set key type: %w", err)
		}
	case *ecdsa.PrivateKey:
		alg = "ES256"
		kty = "EC"
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
		if err := jwkKey.Set(jwk.KeyTypeKey, "EC"); err != nil {
			return nil, fmt.Errorf("failed to set key type: %w", err)
		}
	case ed25519.PrivateKey:
		return nil, fmt.Errorf("unsupported key type: Ed25519 keys are not currently supported")
	default:
		return nil, fmt.Errorf("unsupported key type: %T", g.privateKey)
	}

	if err := jwkKey.Set(jwk.KeyUsageKey, "sig"); err != nil {
		return nil, fmt.Errorf("failed to set key usage: %w", err)
	}

	// Marshal the JWK to JSON and then unmarshal to map to get the actual JWK fields
	jwkJSON, err := json.Marshal(jwkKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JWK: %w", err)
	}

	var jwkMap map[string]interface{}
	if err := json.Unmarshal(jwkJSON, &jwkMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWK to map: %w", err)
	}

	// Build the JWK struct matching the API type, including both RSA and EC fields
	// Fields must be in alphabetical order to match generated type
	use := "sig"
	key := struct {
		Alg *string `json:"alg,omitempty"`
		Crv *string `json:"crv,omitempty"` // EC curve (e.g., "P-256")
		E   *string `json:"e,omitempty"`
		Kid *string `json:"kid,omitempty"`
		Kty *string `json:"kty,omitempty"`
		N   *string `json:"n,omitempty"`
		Use *string `json:"use,omitempty"`
		X   *string `json:"x,omitempty"` // EC x-coordinate
		Y   *string `json:"y,omitempty"` // EC y-coordinate
	}{
		Alg: &alg,
		Kid: &g.keyID,
		Kty: &kty,
		Use: &use,
	}

	// Extract RSA-specific fields
	if e, ok := jwkMap["e"].(string); ok {
		key.E = &e
	}
	if n, ok := jwkMap["n"].(string); ok {
		key.N = &n
	}

	// Extract EC-specific fields
	if crv, ok := jwkMap["crv"].(string); ok {
		key.Crv = &crv
	}
	if x, ok := jwkMap["x"].(string); ok {
		key.X = &x
	}
	if y, ok := jwkMap["y"].(string); ok {
		key.Y = &y
	}

	keys := []struct {
		Alg *string `json:"alg,omitempty"`
		Crv *string `json:"crv,omitempty"` // EC curve
		E   *string `json:"e,omitempty"`
		Kid *string `json:"kid,omitempty"`
		Kty *string `json:"kty,omitempty"`
		N   *string `json:"n,omitempty"`
		Use *string `json:"use,omitempty"`
		X   *string `json:"x,omitempty"` // EC x-coordinate
		Y   *string `json:"y,omitempty"` // EC y-coordinate
	}{key}

	return &pamapi.JWKSResponse{
		Keys: &keys,
	}, nil
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
	case *ecdsa.PrivateKey:
		signingAlg = jwa.ES256
		if err := jwkKey.Set(jwk.AlgorithmKey, jwa.ES256); err != nil {
			return nil, fmt.Errorf("failed to set algorithm: %w", err)
		}
	case ed25519.PrivateKey:
		return nil, fmt.Errorf("unsupported key type: Ed25519 keys are not currently supported")
	default:
		return nil, fmt.Errorf("unsupported key type: %T", g.privateKey)
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

	return identity, nil
}
