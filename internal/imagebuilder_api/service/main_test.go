package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "imagebuilder-service-test")
	if err != nil {
		panic(err)
	}

	key, err := crypto.GenerateAES256Key()
	if err != nil {
		panic(err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
		panic(err)
	}

	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		Keys:        []config.EncryptionKeyConfig{{ID: "test", Path: keyPath}},
		ActiveKeyID: "test",
	}

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	if err := encryption.InitGlobalEncryption(logger, cfg); err != nil {
		panic(err)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
