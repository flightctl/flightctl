package ssh

import (
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"golang.org/x/crypto/ssh"
)

// SSHCryptoSettings contains the SSH crypto algorithm configuration
type SSHCryptoSettings struct {
	KeyExchanges      []string
	Ciphers           []string
	MACs              []string
	HostKeyAlgorithms []string
}

// GetSSHCryptoSettings returns SSH crypto algorithm configuration based on FIPS mode
// detection and configuration overrides.
//
// The algorithm selection follows this precedence:
//  1. Explicit SSH configuration in cfg.CryptoPolicy.SSH (if provided)
//  2. FIPS-compliant algorithms if FIPS mode is detected or forced
//  3. Empty (golang.org/x/crypto/ssh will use defaults)
//
// This ensures that SSH connections use FIPS-approved algorithms when required
// while maintaining backward compatibility in non-FIPS environments.
func GetSSHCryptoSettings(cfg *config.Config) SSHCryptoSettings {
	settings := SSHCryptoSettings{}

	// Determine if we should use FIPS-compliant algorithms
	// Priority: 1) SSH-specific override, 2) Global crypto policy, 3) Auto-detection
	useFIPSAlgorithms := util.IsFIPSEnabled()

	// Check global crypto policy FIPS override
	if cfg != nil && cfg.CryptoPolicy != nil && cfg.CryptoPolicy.ForceFIPSMode != nil {
		useFIPSAlgorithms = *cfg.CryptoPolicy.ForceFIPSMode
	}

	// Check SSH-specific FIPS override (takes precedence over global)
	if cfg != nil && cfg.CryptoPolicy != nil && cfg.CryptoPolicy.SSH != nil && cfg.CryptoPolicy.SSH.ForceFIPSMode != nil {
		useFIPSAlgorithms = *cfg.CryptoPolicy.SSH.ForceFIPSMode
	}

	// Configure Key Exchange Algorithms
	if cfg != nil && cfg.CryptoPolicy != nil && cfg.CryptoPolicy.SSH != nil && len(cfg.CryptoPolicy.SSH.KeyExchangeAlgorithms) > 0 {
		// Use explicitly configured algorithms
		settings.KeyExchanges = cfg.CryptoPolicy.SSH.KeyExchangeAlgorithms
	} else if useFIPSAlgorithms {
		// Use FIPS-compliant algorithms
		settings.KeyExchanges = getFIPSKeyExchangeAlgorithms()
	}
	// else: use golang.org/x/crypto/ssh defaults (empty slice)

	// Configure Ciphers
	if cfg != nil && cfg.CryptoPolicy != nil && cfg.CryptoPolicy.SSH != nil && len(cfg.CryptoPolicy.SSH.Ciphers) > 0 {
		settings.Ciphers = cfg.CryptoPolicy.SSH.Ciphers
	} else if useFIPSAlgorithms {
		settings.Ciphers = getFIPSCiphers()
	}

	// Configure MACs (Message Authentication Codes)
	if cfg != nil && cfg.CryptoPolicy != nil && cfg.CryptoPolicy.SSH != nil && len(cfg.CryptoPolicy.SSH.MACs) > 0 {
		settings.MACs = cfg.CryptoPolicy.SSH.MACs
	} else if useFIPSAlgorithms {
		settings.MACs = getFIPSMACs()
	}

	// Configure Host Key Algorithms
	if cfg != nil && cfg.CryptoPolicy != nil && cfg.CryptoPolicy.SSH != nil && len(cfg.CryptoPolicy.SSH.HostKeyAlgorithms) > 0 {
		settings.HostKeyAlgorithms = cfg.CryptoPolicy.SSH.HostKeyAlgorithms
	} else if useFIPSAlgorithms {
		settings.HostKeyAlgorithms = getFIPSHostKeyAlgorithms()
	}

	return settings
}

// ApplyCryptoSettingsToClientConfig applies crypto settings to an ssh.ClientConfig
func (s *SSHCryptoSettings) ApplyCryptoSettingsToClientConfig(cfg *ssh.ClientConfig) {
	if cfg == nil {
		return
	}

	if len(s.KeyExchanges) > 0 {
		cfg.Config.KeyExchanges = s.KeyExchanges
	}

	if len(s.Ciphers) > 0 {
		cfg.Config.Ciphers = s.Ciphers
	}

	if len(s.MACs) > 0 {
		cfg.Config.MACs = s.MACs
	}

	if len(s.HostKeyAlgorithms) > 0 {
		cfg.HostKeyAlgorithms = s.HostKeyAlgorithms
	}
}

// getFIPSKeyExchangeAlgorithms returns FIPS 140-2 compliant key exchange algorithms.
//
// These algorithms are based on:
// - ArgoCD's FIPS implementation (https://github.com/argoproj/argo-cd/pull/24086)
// - NIST FIPS 140-2 approved algorithms
//
// Excluded non-FIPS algorithms:
// - curve25519-sha256 (causes panics in FIPS mode)
// - diffie-hellman-group1-sha1 (SHA-1 not approved for key exchange in FIPS 140-2)
func getFIPSKeyExchangeAlgorithms() []string {
	return []string{
		"ecdh-sha2-nistp256",
		"ecdh-sha2-nistp384",
		"ecdh-sha2-nistp521",
		"diffie-hellman-group-exchange-sha256",
		"diffie-hellman-group14-sha256",
	}
}

// getFIPSCiphers returns FIPS 140-2 compliant ciphers for SSH connections.
//
// These are AES-based ciphers using approved modes (GCM, CTR).
// Excluded: ChaCha20-Poly1305 and other non-AES ciphers.
func getFIPSCiphers() []string {
	return []string{
		"aes128-gcm@openssh.com",
		"aes256-gcm@openssh.com",
		"aes128-ctr",
		"aes192-ctr",
		"aes256-ctr",
	}
}

// getFIPSMACs returns FIPS 140-2 compliant MAC algorithms.
//
// These use SHA-2 family hash functions (SHA-256, SHA-512).
// Excluded: SHA-1 based MACs (not approved in FIPS 140-2).
func getFIPSMACs() []string {
	return []string{
		"hmac-sha2-256-etm@openssh.com",
		"hmac-sha2-512-etm@openssh.com",
		"hmac-sha2-256",
		"hmac-sha2-512",
	}
}

// getFIPSHostKeyAlgorithms returns FIPS 140-2 compliant host key algorithms.
//
// These include ECDSA with NIST curves and RSA with SHA-2.
// Excluded:
// - ssh-rsa (uses SHA-1)
// - ssh-ed25519 (Ed25519 not FIPS approved)
func getFIPSHostKeyAlgorithms() []string {
	return []string{
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
		"rsa-sha2-256",
		"rsa-sha2-512",
	}
}
