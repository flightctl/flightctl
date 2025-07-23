package otel_collector

import (
	"github.com/flightctl/flightctl/internal/config"
)

// ToOTelYAMLConfig converts the OpenTelemetry collector configuration to YAML format
func ToOTelYAMLConfig(cfg *config.Config) map[string]interface{} {
	if cfg.OTelCollector == nil {
		return nil
	}

	yamlConfig := make(map[string]interface{})

	// Receivers
	if cfg.OTelCollector.OTLP != nil {
		otlpConfig := map[string]interface{}{
			"protocols": map[string]interface{}{
				"grpc": map[string]interface{}{
					"endpoint": cfg.OTelCollector.OTLP.Endpoint,
				},
			},
		}

		if cfg.OTelCollector.OTLP.TLS != nil {
			grpcConfig := otlpConfig["protocols"].(map[string]interface{})["grpc"].(map[string]interface{})
			grpcConfig["tls"] = map[string]interface{}{
				"cert_file":      cfg.OTelCollector.OTLP.TLS.CertFile,
				"key_file":       cfg.OTelCollector.OTLP.TLS.KeyFile,
				"client_ca_file": cfg.OTelCollector.OTLP.TLS.ClientCAFile,
			}
		}

		if cfg.OTelCollector.OTLP.Auth != nil {
			grpcConfig := otlpConfig["protocols"].(map[string]interface{})["grpc"].(map[string]interface{})
			grpcConfig["auth"] = map[string]interface{}{
				"authenticator": cfg.OTelCollector.OTLP.Auth.Authenticator,
			}
		}

		yamlConfig["receivers"] = map[string]interface{}{
			"otlp": otlpConfig,
		}
	}

	// Processors
	yamlConfig["processors"] = map[string]interface{}{
		"deviceid": map[string]interface{}{},
		"transform": map[string]interface{}{
			"metric_statements": []map[string]interface{}{
				{
					"context": "datapoint",
					"statements": []string{
						"set(attributes[\"device_id\"], resource.attributes[\"device_id\"])",
						"set(attributes[\"org_id\"], resource.attributes[\"org_id\"])",
					},
				},
			},
		},
	}

	// Exporters
	if cfg.OTelCollector.Prometheus != nil {
		yamlConfig["exporters"] = map[string]interface{}{
			"prometheus": map[string]interface{}{
				"endpoint": cfg.OTelCollector.Prometheus.Endpoint,
			},
		}
	}

	// Extensions
	if cfg.OTelCollector.Extensions != nil && cfg.OTelCollector.Extensions.CNAuthenticator != nil {
		yamlConfig["extensions"] = map[string]interface{}{
			"cnauthenticator": map[string]interface{}{
				"print_cn":  cfg.OTelCollector.Extensions.CNAuthenticator.PrintCN,
				"log_level": cfg.OTelCollector.Extensions.CNAuthenticator.LogLevel,
			},
		}
	}

	// Service
	if cfg.OTelCollector.Pipelines != nil && cfg.OTelCollector.Pipelines.Metrics != nil {
		serviceConfig := map[string]interface{}{
			"pipelines": map[string]interface{}{
				"metrics": map[string]interface{}{
					"receivers":  cfg.OTelCollector.Pipelines.Metrics.Receivers,
					"processors": cfg.OTelCollector.Pipelines.Metrics.Processors,
					"exporters":  cfg.OTelCollector.Pipelines.Metrics.Exporters,
				},
			},
		}

		// Add extensions to service if configured
		if cfg.OTelCollector.Extensions != nil && cfg.OTelCollector.Extensions.CNAuthenticator != nil {
			serviceConfig["extensions"] = []string{"cnauthenticator"}
		}

		yamlConfig["service"] = serviceConfig
	}

	return yamlConfig
}
