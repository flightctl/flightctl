package ca_config

type CAIdType int

const (
	InternalCA CAIdType = iota + 1
	AsyncInternalCA
)

type InternalCACfg struct {
	CaCertFile         string `json:"caCertFile,omitempty"`
	CaKeyFile          string `json:"caKeyFile,omitempty"`
	SignerCertName     string `json:"SignerCertName,omitempty"`
	CaSerialFile       string `json:"caSerialFile,omitempty"`
	CaCertValidityDays int    `json:"CaCertValidityDays,omitempty"`
	CACertStore        string `json:"CACertStore,omitempty"`
}

type CAConfigType struct {
	CAType                          CAIdType       `json:"CAType,omitempty"`
	AdminCommonName                 string         `json:"AdminCommonName,omitempty"`
	ClientBootstrapCommonName       string         `json:"ClientBootstrapCommonName,omitempty"`
	ClientBootstrapCertName         string         `json:"ClientBootstrapCertName,omitempty"`
	ClientBootstrapCommonNamePrefix string         `json:"ClientBootstrapCommonNamePrefix,omitempty"`
	ClientBootstrapValidityDays     int            `json:"ClientBootStrapValidityDays,omitempty"`
	DeviceCommonNamePrefix          string         `json:"DeviceCommonNamePrefix,omitempty"`
	InternalCAConfig                *InternalCACfg `json:"InternalCAConfig,omitempty"`
	ServerCertValidityDays          int            `json:"ServerCertValidityDays,omitempty"`
	ExtraAllowedPrefixes            []string       `json:"ExtraAllowedPrefixes,omitempty"`
}

func NewDefault(certStore string) *CAConfigType {
	c := &CAConfigType{
		CAType:                          InternalCA,
		AdminCommonName:                 "flightctl-admin",
		ClientBootstrapCertName:         "client-enrollment",
		ClientBootstrapCommonName:       "client-enrollment",
		ClientBootstrapCommonNamePrefix: "client-enrollment-",
		ClientBootstrapValidityDays:     1,
		DeviceCommonNamePrefix:          "device:",
		InternalCAConfig: &InternalCACfg{
			CaCertFile:         "ca.crt",
			CaKeyFile:          "ca.key",
			CaCertValidityDays: 365,
			SignerCertName:     "ca",
			CACertStore:        certStore,
		},
	}
	return c
}
