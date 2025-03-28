package ca

type CAIdType int

const (
	InternalCA CAIdType = iota + 1
	AsyncInternalCA
)

type InternalCfg struct {
	CertFile         string `json:"certFile,omitempty"`
	KeyFile          string `json:"keyFile,omitempty"`
	SignerCertName   string `json:"signerCertName,omitempty"`
	SerialFile       string `json:"serialFile,omitempty"`
	CertValidityDays int    `json:"certValidityDays,omitempty"`
	CertStore        string `json:"certStore,omitempty"`
}

type Config struct {
	CAType                          CAIdType     `json:"type,omitempty"`
	AdminCommonName                 string       `json:"adminCommonName,omitempty"`
	ClientBootstrapCommonName       string       `json:"clientBootstrapCommonName,omitempty"`
	ClientBootstrapCertName         string       `json:"clientBootstrapCertName,omitempty"`
	ClientBootstrapSignerName       string       `json:"clientBootstrapSignerName,omitempty"`
	ClientBootstrapCommonNamePrefix string       `json:"clientBootstrapCommonNamePrefix,omitempty"`
	ClientBootstrapValidityDays     int          `json:"clientBootStrapValidityDays,omitempty"`
	DeviceCommonNamePrefix          string       `json:"deviceCommonNamePrefix,omitempty"`
	InternalConfig                  *InternalCfg `json:"internalConfig,omitempty"`
	ServerCertValidityDays          int          `json:"serverCertValidityDays,omitempty"`
	ExtraAllowedPrefixes            []string     `json:"extraAllowedPrefixes,omitempty"`
}

func NewDefault(tempDir string) *Config {
	c := &Config{
		CAType:                          InternalCA,
		AdminCommonName:                 "flightctl-admin",
		ClientBootstrapCertName:         "client-enrollment",
		ClientBootstrapCommonName:       "client-enrollment",
		ClientBootstrapSignerName:       "client-enrollment",
		ClientBootstrapCommonNamePrefix: "client-enrollment-",
		ClientBootstrapValidityDays:     365,
		ServerCertValidityDays:          365,
		DeviceCommonNamePrefix:          "device:",
		InternalConfig: &InternalCfg{
			CertFile:         "ca.crt",
			KeyFile:          "ca.key",
			CertValidityDays: 3650,
			SignerCertName:     "ca",
			CertStore:        tempDir,
		},
	}
	return c
}
