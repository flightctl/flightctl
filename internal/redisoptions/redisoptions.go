package redisoptions

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/redis/go-redis/v9"
)

func ConfigToRedisOptions(cfg *config.Config) (*redis.Options, error) {
	options := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.KV.Hostname, cfg.KV.Port),
		Password: cfg.KV.Password,
		DB:       0,
	}

	if cfg.KV.CaCertFile != "" {
		tlsConfig, err := loadTLSConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to configure TLS for redis: %w", err)
		}
		options.TLSConfig = tlsConfig
	}

	return options, nil
}

func loadTLSConfig(cfg *config.Config) (*tls.Config, error) {
	caCert, err := os.ReadFile(cfg.KV.CaCertFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert file: %w", err)
	}

	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(caCert); !ok {
		return nil, errors.New("failed to append CA cert")
	}

	clientCerts := []tls.Certificate{}
	if cfg.KV.CertFile != "" {
		clientCert, err := tls.LoadX509KeyPair(cfg.KV.CertFile, cfg.KV.KeyFile)
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
