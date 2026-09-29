package httpsource

// Config holds the provider-specific configuration for the HTTP snapshot source.
type Config struct {
	ListenAddress string `json:"listenAddress,omitempty"`
	Path          string `json:"path,omitempty"`
}
