package common

import (
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

// BaseServiceConfig holds common HTTP service configuration fields.
type BaseServiceConfig struct {
	Address               string              `json:"address,omitempty"`
	LogLevel              string              `json:"logLevel,omitempty"`
	HttpReadTimeout       util.Duration       `json:"httpReadTimeout,omitempty"`
	HttpReadHeaderTimeout util.Duration       `json:"httpReadHeaderTimeout,omitempty"`
	HttpWriteTimeout      util.Duration       `json:"httpWriteTimeout,omitempty"`
	HttpIdleTimeout       util.Duration       `json:"httpIdleTimeout,omitempty"`
	HttpMaxNumHeaders     int                 `json:"httpMaxNumHeaders,omitempty"`
	HttpMaxHeaderBytes    int                 `json:"httpMaxHeaderBytes,omitempty"`
	HttpMaxUrlLength      int                 `json:"httpMaxUrlLength,omitempty"`
	HttpMaxRequestSize    int                 `json:"httpMaxRequestSize,omitempty"`
	RateLimit             *RateLimitConfig    `json:"rateLimit,omitempty"`
	HealthChecks          *HealthChecksConfig `json:"healthChecks,omitempty"`
}

// NewDefaultBaseService returns a default base service configuration.
func NewDefaultBaseService() *BaseServiceConfig {
	return &BaseServiceConfig{
		Address:               ":3443",
		LogLevel:              "info",
		HttpReadTimeout:       util.Duration(5 * time.Minute),
		HttpReadHeaderTimeout: util.Duration(5 * time.Minute),
		HttpWriteTimeout:      util.Duration(5 * time.Minute),
		HttpIdleTimeout:       util.Duration(5 * time.Minute),
		HttpMaxNumHeaders:     32,
		HttpMaxHeaderBytes:    32 * 1024, // 32KB
		HttpMaxUrlLength:      2000,
		HttpMaxRequestSize:    50 * 1024 * 1024, // 50MB
		HealthChecks:          NewDefaultHealthChecks(),
	}
}
