package ssh

import (
	"os"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestGetSSHCryptoSettings_FIPSMode(t *testing.T) {
	require := require.New(t)

	// Save and restore environment
	origOpenSSL, origSet := os.LookupEnv("OPENSSL_FORCE_FIPS_MODE")
	defer func() {
		if origSet {
			os.Setenv("OPENSSL_FORCE_FIPS_MODE", origOpenSSL)
		} else {
			os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
		}
		util.ResetFIPSCache()
	}()

	// Force FIPS mode via environment
	os.Setenv("OPENSSL_FORCE_FIPS_MODE", "1")
	util.ResetFIPSCache()

	// Get SSH config with nil config (should use FIPS defaults)
	settings := GetSSHCryptoSettings(nil)

	// Verify FIPS-compliant algorithms are selected
	require.NotEmpty(settings.KeyExchanges, "Key exchanges should be set in FIPS mode")
	require.NotEmpty(settings.Ciphers, "Ciphers should be set in FIPS mode")
	require.NotEmpty(settings.MACs, "MACs should be set in FIPS mode")
	require.NotEmpty(settings.HostKeyAlgorithms, "Host key algorithms should be set in FIPS mode")

	// Verify specific FIPS algorithms are present
	require.Contains(settings.KeyExchanges, "ecdh-sha2-nistp256")
	require.Contains(settings.Ciphers, "aes256-gcm@openssh.com")
	require.Contains(settings.MACs, "hmac-sha2-256")
	require.Contains(settings.HostKeyAlgorithms, "rsa-sha2-256")

	// Verify non-FIPS algorithms are NOT present
	require.NotContains(settings.KeyExchanges, "curve25519-sha256", "curve25519 should not be in FIPS mode")
	require.NotContains(settings.HostKeyAlgorithms, "ssh-ed25519", "Ed25519 should not be in FIPS mode")
}

func TestGetSSHCryptoSettings_NonFIPSMode(t *testing.T) {
	require := require.New(t)

	// Ensure FIPS mode is NOT active via environment variables
	origOpenSSL, origOpenSSLSet := os.LookupEnv("OPENSSL_FORCE_FIPS_MODE")
	origGolang, origGolangSet := os.LookupEnv("GOLANG_FIPS")
	defer func() {
		if origOpenSSLSet {
			os.Setenv("OPENSSL_FORCE_FIPS_MODE", origOpenSSL)
		} else {
			os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
		}
		if origGolangSet {
			os.Setenv("GOLANG_FIPS", origGolang)
		} else {
			os.Unsetenv("GOLANG_FIPS")
		}
		util.ResetFIPSCache()
	}()

	os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
	os.Unsetenv("GOLANG_FIPS")
	util.ResetFIPSCache()

	// Check if FIPS is still active despite unsetting env vars
	// This would mean the host OS itself enforces FIPS mode
	if util.IsFIPSEnabled() {
		t.Skip("Skipping non-FIPS mode test: host OS enforces FIPS mode via crypto/fips140 or /proc/sys/crypto/fips_enabled")
	}

	// Get SSH config with nil config (should use library defaults)
	settings := GetSSHCryptoSettings(nil)

	// In non-FIPS mode with nil config, settings should be empty
	// which allows golang.org/x/crypto/ssh to use its own defaults
	require.Empty(settings.KeyExchanges, "Should use library defaults when FIPS is not active")
	require.Empty(settings.Ciphers, "Should use library defaults when FIPS is not active")
	require.Empty(settings.MACs, "Should use library defaults when FIPS is not active")
	require.Empty(settings.HostKeyAlgorithms, "Should use library defaults when FIPS is not active")
}

func TestGetSSHCryptoSettings_ExplicitFIPSForce(t *testing.T) {
	require := require.New(t)

	// Ensure environment doesn't force FIPS
	origOpenSSL, origSet := os.LookupEnv("OPENSSL_FORCE_FIPS_MODE")
	defer func() {
		if origSet {
			os.Setenv("OPENSSL_FORCE_FIPS_MODE", origOpenSSL)
		} else {
			os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
		}
		util.ResetFIPSCache()
	}()
	os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
	util.ResetFIPSCache()

	// Create config that explicitly forces FIPS mode
	cfg := &config.Config{
		CryptoPolicy: &config.CryptoPolicyConfig{
			SSH: &config.SSHCryptoConfig{
				ForceFIPSMode: lo.ToPtr(true),
			},
		},
	}

	settings := GetSSHCryptoSettings(cfg)

	// Should use FIPS algorithms even if environment detection says otherwise
	require.NotEmpty(settings.KeyExchanges)
	require.Contains(settings.KeyExchanges, "ecdh-sha2-nistp256")
	require.NotContains(settings.KeyExchanges, "curve25519-sha256")
}

func TestGetSSHCryptoSettings_ExplicitFIPSDisable(t *testing.T) {
	require := require.New(t)

	// Force FIPS mode via environment
	origOpenSSL, origSet := os.LookupEnv("OPENSSL_FORCE_FIPS_MODE")
	defer func() {
		if origSet {
			os.Setenv("OPENSSL_FORCE_FIPS_MODE", origOpenSSL)
		} else {
			os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
		}
		util.ResetFIPSCache()
	}()
	os.Setenv("OPENSSL_FORCE_FIPS_MODE", "1")
	util.ResetFIPSCache()

	// Create config that explicitly disables FIPS mode
	cfg := &config.Config{
		CryptoPolicy: &config.CryptoPolicyConfig{
			SSH: &config.SSHCryptoConfig{
				ForceFIPSMode: lo.ToPtr(false),
			},
		},
	}

	settings := GetSSHCryptoSettings(cfg)

	// Should NOT set FIPS algorithms even though environment detection says FIPS is active
	// Empty slices mean golang.org/x/crypto/ssh will use its defaults
	require.Empty(settings.KeyExchanges, "Should use library defaults when FIPS is explicitly disabled")
	require.Empty(settings.Ciphers, "Should use library defaults when FIPS is explicitly disabled")
}

func TestGetSSHCryptoSettings_ManualOverride(t *testing.T) {
	require := require.New(t)

	// Create config with manual algorithm specification
	customKex := []string{"custom-kex-1", "custom-kex-2"}
	customCiphers := []string{"custom-cipher-1"}
	customMACs := []string{"custom-mac-1", "custom-mac-2"}
	customHostKeys := []string{"custom-hostkey-1"}

	cfg := &config.Config{
		CryptoPolicy: &config.CryptoPolicyConfig{
			SSH: &config.SSHCryptoConfig{
				KeyExchangeAlgorithms: customKex,
				Ciphers:               customCiphers,
				MACs:                  customMACs,
				HostKeyAlgorithms:     customHostKeys,
			},
		},
	}

	settings := GetSSHCryptoSettings(cfg)

	// Should use explicitly configured algorithms, not FIPS defaults
	require.Equal(customKex, settings.KeyExchanges)
	require.Equal(customCiphers, settings.Ciphers)
	require.Equal(customMACs, settings.MACs)
	require.Equal(customHostKeys, settings.HostKeyAlgorithms)
}

func TestGetSSHCryptoSettings_PartialManualOverride(t *testing.T) {
	require := require.New(t)

	// Force FIPS mode
	origOpenSSL, origSet := os.LookupEnv("OPENSSL_FORCE_FIPS_MODE")
	defer func() {
		if origSet {
			os.Setenv("OPENSSL_FORCE_FIPS_MODE", origOpenSSL)
		} else {
			os.Unsetenv("OPENSSL_FORCE_FIPS_MODE")
		}
		util.ResetFIPSCache()
	}()
	os.Setenv("OPENSSL_FORCE_FIPS_MODE", "1")
	util.ResetFIPSCache()

	// Create config with only some algorithms specified
	customKex := []string{"custom-kex-1", "custom-kex-2"}

	cfg := &config.Config{
		CryptoPolicy: &config.CryptoPolicyConfig{
			SSH: &config.SSHCryptoConfig{
				KeyExchangeAlgorithms: customKex,
				// Other fields left empty - should use FIPS defaults
			},
		},
	}

	settings := GetSSHCryptoSettings(cfg)

	// Should use custom key exchange but FIPS defaults for others
	require.Equal(customKex, settings.KeyExchanges, "Should use custom key exchange")
	require.NotEmpty(settings.Ciphers, "Should use FIPS ciphers")
	require.Contains(settings.Ciphers, "aes256-gcm@openssh.com", "Should contain FIPS cipher")
}

func TestFIPSKeyExchangeAlgorithms(t *testing.T) {
	require := require.New(t)

	algorithms := getFIPSKeyExchangeAlgorithms()

	// Verify expected FIPS algorithms are present
	expectedAlgorithms := []string{
		"ecdh-sha2-nistp256",
		"ecdh-sha2-nistp384",
		"ecdh-sha2-nistp521",
		"diffie-hellman-group-exchange-sha256",
		"diffie-hellman-group14-sha256",
	}

	for _, expected := range expectedAlgorithms {
		require.Contains(algorithms, expected, "FIPS key exchange should include %s", expected)
	}

	// Verify non-FIPS algorithms are absent
	forbiddenAlgorithms := []string{
		"curve25519-sha256",
		"diffie-hellman-group1-sha1", // SHA-1 not FIPS approved
	}

	for _, forbidden := range forbiddenAlgorithms {
		require.NotContains(algorithms, forbidden, "FIPS key exchange should NOT include %s", forbidden)
	}
}

func TestFIPSCiphers(t *testing.T) {
	require := require.New(t)

	ciphers := getFIPSCiphers()

	// Verify all ciphers are AES-based
	for _, cipher := range ciphers {
		require.Contains(cipher, "aes", "FIPS ciphers should be AES-based, got: %s", cipher)
	}

	// Verify ChaCha20 is not included (not FIPS approved)
	for _, cipher := range ciphers {
		require.NotContains(cipher, "chacha20", "FIPS ciphers should not include ChaCha20")
	}
}

func TestFIPSMACs(t *testing.T) {
	require := require.New(t)

	macs := getFIPSMACs()

	// Verify all MACs use SHA-2
	for _, mac := range macs {
		require.Contains(mac, "sha2", "FIPS MACs should use SHA-2, got: %s", mac)
	}

	// Verify SHA-1 MACs are not included
	forbiddenMACs := []string{
		"hmac-sha1",
		"hmac-sha1-96",
	}

	for _, forbidden := range forbiddenMACs {
		require.NotContains(macs, forbidden, "FIPS MACs should not include %s", forbidden)
	}
}

func TestFIPSHostKeyAlgorithms(t *testing.T) {
	require := require.New(t)

	algorithms := getFIPSHostKeyAlgorithms()

	// Verify ECDSA and RSA-SHA2 are included
	expectedAlgorithms := []string{
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
		"rsa-sha2-256",
		"rsa-sha2-512",
	}

	for _, expected := range expectedAlgorithms {
		require.Contains(algorithms, expected, "FIPS host keys should include %s", expected)
	}

	// Verify non-FIPS algorithms are absent
	forbiddenAlgorithms := []string{
		"ssh-rsa",     // Uses SHA-1
		"ssh-ed25519", // Ed25519 not FIPS approved
		"ssh-dss",     // DSA deprecated
	}

	for _, forbidden := range forbiddenAlgorithms {
		require.NotContains(algorithms, forbidden, "FIPS host keys should NOT include %s", forbidden)
	}
}
