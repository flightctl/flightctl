package kvconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

type KvConfig struct {
	Hostname   string `json:"hostname,omitempty"`
	Port       uint   `json:"port,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	CaCertFile string `json:"caCertFile,omitempty"`
	CertFile   string `json:"certFile,omitempty"`
	KeyFile    string `json:"keyFile,omitempty"`
	DB         int    `json:"db,omitempty"`
}

func ConfigToRedisOptions(cfg *KvConfig) (*redis.Options, error) {
	options := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Hostname, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	}

	if cfg.CaCertFile != "" {
		tlsConfig, err := loadTLSConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to configure TLS for redis: %w", err)
		}
		options.TLSConfig = tlsConfig
	}

	return options, nil
}

func loadTLSConfig(cfg *KvConfig) (*tls.Config, error) {
	caCert, err := os.ReadFile(cfg.CaCertFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert file: %w", err)
	}

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(caCert); !ok {
		return nil, errors.New("failed to append CA cert")
	}

	clientCerts := []tls.Certificate{}
	if cfg.CertFile != "" {
		clientCert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read client cert/key: %w", err)
		}
		clientCerts = append(clientCerts, clientCert)
	}

	return &tls.Config{
		Certificates: clientCerts,
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
