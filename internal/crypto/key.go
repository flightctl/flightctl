package crypto

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func NewKeyPair() (crypto.PublicKey, crypto.PrivateKey, error) {
	return newECDSAKeyPair()
}

func NewKeyPairWithHash() (crypto.PublicKey, crypto.PrivateKey, []byte, error) {
	publicKey, privateKey, err := newECDSAKeyPair()
	var publicKeyHash []byte
	if err == nil {
		hash := sha256.New()
		hash.Write(publicKey.X.Bytes())
		publicKeyHash = hash.Sum(nil)
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

func EnsureKey(keyFile string) (*crypto.PrivateKey, bool, error) {
	if key, err := LoadKey(keyFile); err == nil {
		return key, false, err
	}
	_, privateKey, _ := NewKeyPair()
	if err := WriteKey(keyFile, privateKey); err != nil {
		return nil, false, err
	}
	return &privateKey, true, nil
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

func PEMEncodeKey(key crypto.PrivateKey) ([]byte, error) {
	b := bytes.Buffer{}
	switch key := key.(type) {
	case *ecdsa.PrivateKey:
		keyBytes, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return []byte{}, err
		}
		if err := pem.Encode(&b, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
			return b.Bytes(), err
		}
	default:
		return []byte{}, errors.New("unsupported key type")

	}
	return b.Bytes(), nil
}

func LoadKey(keyFile string) (*crypto.PrivateKey, error) {
	pemBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	key, err := ParseKeyPEM(pemBlock)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %v", keyFile, err)
	}
	return &key, nil
}

func ParseKeyPEM(pemKey []byte) (crypto.PrivateKey, error) {
	block, rest := pem.Decode(pemKey)
	switch {
	case block == nil:
		return nil, fmt.Errorf("not a valid PEM encoded block")
	case len(bytes.TrimSpace(rest)) > 0:
		return nil, fmt.Errorf("not a valid PEM encoded block")
	case block.Headers["Proc-Type"] == "4,ENCRYPTED" || block.Type == "ENCRYPTED PRIVATE KEY":
		return nil, fmt.Errorf("encrypted PEM private key")
	}

	var key crypto.PrivateKey
	var err error
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
		return nil, fmt.Errorf("error parsing private key: %v", err)
	}
	return key, nil
}
