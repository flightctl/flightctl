package crypto

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/secure-systems-lab/go-securesystemslib/encrypted"
)

func NewKeyPair() (crypto.PublicKey, crypto.PrivateKey, error) {
	return newECDSAKeyPair()
}

func NewKeyPairWithHash() (crypto.PublicKey, crypto.PrivateKey, []byte, error) {
	publicKey, privateKey, err := newECDSAKeyPair()
	var publicKeyHash []byte
	if err == nil {
		publicKeyHash = hashECDSAKey(publicKey)
	}
	return publicKey, privateKey, publicKeyHash, nil
}

func newECDSAKeyPair() (*ecdsa.PublicKey, *ecdsa.PrivateKey, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return &privateKey.PublicKey, privateKey, nil
}

func HashPublicKey(key crypto.PublicKey) ([]byte, error) {
	switch key := key.(type) {
	case ecdsa.PublicKey:
		return hashECDSAKey(&key), nil
	case *ecdsa.PublicKey:
		return hashECDSAKey(key), nil
	case *crypto.PublicKey:
		return HashPublicKey(*key)
	case *crypto.PrivateKey:
		privateKey, ok := (*key).(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("unsupported private key type %T", key)
		}
		return HashPublicKey(privateKey.Public())
	default:
		return nil, fmt.Errorf("unsupported public key type %T", key)
	}
}

func hashECDSAKey(publicKey *ecdsa.PublicKey) []byte {
	hash := sha256.New()
	hash.Write(publicKey.X.Bytes())
	hash.Write(publicKey.Y.Bytes())
	return hash.Sum(nil)
}

func EnsureKey(keyFile string) (crypto.PublicKey, crypto.PrivateKey, bool, error) {
	if privateKey, err := LoadKey(keyFile); err == nil {
		privateKeySigner, ok := privateKey.(crypto.Signer)
		if !ok {
			return nil, nil, false, err
		}
		publicKey := privateKeySigner.Public()
		return publicKey, privateKey, false, err
	}
	publicKey, privateKey, _ := NewKeyPair()
	if err := WriteKey(keyFile, privateKey); err != nil {
		return nil, nil, false, err
	}
	return publicKey, privateKey, true, nil
}

func WriteKey(keyPath string, key crypto.PrivateKey) error {
	keyPEM, err := PEMEncodeKey(key)
	if err != nil {
		return fmt.Errorf("PEM encoding private key: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), os.FileMode(0755)); err != nil {
		return fmt.Errorf("creating directory for private key: %v", err)
	}
	return os.WriteFile(keyPath, keyPEM, os.FileMode(0600))
}

// this copies functionality from sigstore's cosign to encrypt the private key using functionality
// from secure systems lab, which relies on golang crypto's secretbox and scrypt. see:
// https://github.com/sigstore/cosign/blob/77f71e0d7470e31ed4ed5653fe5a7c8e3b283606/pkg/cosign/keys.go#L158
// https://github.com/secure-systems-lab/go-securesystemslib/blob/7dd9eabdaf9ea98ba33653cdfbdec7057bd662fd/encrypted/encrypted.go#L158
func WritePasswordEncryptedKey(keyPath string, key crypto.PrivateKey, password []byte) error {
	if len(password) == 0 {
		return WriteKey(keyPath, key)
	}

	keyPEM, err := PEMEncodeKey(key)
	if err != nil {
		return fmt.Errorf("PEM encoding private key: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), os.FileMode(0755)); err != nil {
		return fmt.Errorf("creating directory for private key: %v", err)
	}

	encBytes, err := encrypted.Encrypt(keyPEM, password)
	if err != nil {
		return fmt.Errorf("encrypting private key: %w", err)
	}
	privBytes := pem.EncodeToMemory(&pem.Block{
		Bytes: encBytes,
		Type:  "ENCRYPTED PRIVATE KEY",
	})
	return os.WriteFile(keyPath, privBytes, os.FileMode(0600))
}

func PEMEncodeKey(key crypto.PrivateKey) ([]byte, error) {
	b := bytes.Buffer{}
	var keyBytes []byte
	var err error
	var pemType string

	switch key := key.(type) {
	case *ecdsa.PrivateKey:
		keyBytes, err = x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal ECDSA private key: %w", err)
		}
		pemType = "EC PRIVATE KEY"
	case *rsa.PrivateKey:
		keyBytes = x509.MarshalPKCS1PrivateKey(key)
		pemType = "RSA PRIVATE KEY"
	default:
		keyBytes, err = x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal PKCS8 private key: %w", err)
		}
		pemType = "PRIVATE KEY"
	}

	if err := pem.Encode(&b, &pem.Block{Type: pemType, Bytes: keyBytes}); err != nil {
		return nil, fmt.Errorf("failed to encode %s: %w", pemType, err)
	}
	return b.Bytes(), nil
}

func LoadKey(keyFile string) (crypto.PrivateKey, error) {
	pemBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	key, err := ParseKeyPEM(pemBlock)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %v", keyFile, err)
	}
	return key, nil
}

func IsEncryptedPEMKey(pemKey []byte) (bool, error) {
	block, err := GetPEMBlock(pemKey)
	if err != nil {
		return false, err
	}
	if block.Headers["Proc-Type"] == "4,ENCRYPTED" || block.Type == "ENCRYPTED PRIVATE KEY" {
		return true, nil
	}

	return false, nil
}

func GetPEMBlock(pemKey []byte) (*pem.Block, error) {
	block, rest := pem.Decode(pemKey)
	switch {
	case block == nil:
		return nil, fmt.Errorf("not a valid PEM encoded block")
	case len(bytes.TrimSpace(rest)) > 0:
		return nil, fmt.Errorf("not a valid PEM encoded block")
	default:
		return block, nil
	}
}

func DecryptKeyBytes(pemKeyEncrypted []byte, pw []byte) ([]byte, error) {
	block, err := GetPEMBlock(pemKeyEncrypted)
	if err != nil {
		return nil, err
	}

	decrypted, err := encrypted.Decrypt(block.Bytes, pw)
	if err != nil {
		return nil, fmt.Errorf("decrypting key: %w", err)
	}

	return decrypted, nil
}

func ParseKeyPEM(pemKey []byte) (crypto.PrivateKey, error) {
	var key crypto.PrivateKey
	var err error

	block, err := GetPEMBlock(pemKey)
	if err != nil {
		return nil, err
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unknown PEM private key type: %s", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %v", err)
	}
	return key, nil
}

func GetExtensionValue(cert *x509.Certificate, oid asn1.ObjectIdentifier) (string, error) {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oid) {
			var value string
			if _, err := asn1.Unmarshal(ext.Value, &value); err != nil {
				return "", fmt.Errorf("failed to unmarshal extension for OID %v: %w", oid, err)
			}
			return value, nil
		}
	}

	// Fallback: also check ExtraExtensions (if needed)
	for _, ext := range cert.ExtraExtensions {
		if ext.Id.Equal(oid) {
			var value string
			if _, err := asn1.Unmarshal(ext.Value, &value); err != nil {
				return "", fmt.Errorf("failed to unmarshal extension for OID %v: %w", oid, err)
			}
			return value, nil
		}
	}

	return "", flterrors.ErrExtensionNotFound
}
