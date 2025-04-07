package ca_config

type CAIdType int

const (
	InternalCA CAIdType = iota + 1
	AsyncInternalCA
)

type InternalCACfg struct {
	CaCertFile         string `json:"caCertFile,omitempty"`
	CaKeyFile          string `json:"caKeyFile,omitempty"`
	SignerCertName     string `json:"signerCertName,omitempty"`
	CaSerialFile       string `json:"caSerialFile,omitempty"`
	CaCertValidityDays int    `json:"caCertValidityDays,omitempty"`
	CACertStore        string `json:"cACertStore,omitempty"`
}

type CAConfigType struct {
	CAType                          CAIdType       `json:"type,omitempty"`
	AdminCommonName                 string         `json:"adminCommonName,omitempty"`
	ClientBootstrapCommonName       string         `json:"clientBootstrapCommonName,omitempty"`
	ClientBootstrapCertName         string         `json:"clientBootstrapCertName,omitempty"`
	ClientBootstrapCommonNamePrefix string         `json:"clientBootstrapCommonNamePrefix,omitempty"`
	ClientBootstrapValidityDays     int            `json:"clientBootStrapValidityDays,omitempty"`
	DeviceCommonNamePrefix          string         `json:"deviceCommonNamePrefix,omitempty"`
	InternalCAConfig                *InternalCACfg `json:"internalCAConfig,omitempty"`
	ServerCertValidityDays          int            `json:"serverCertValidityDays,omitempty"`
	ExtraAllowedPrefixes            []string       `json:"extraAllowedPrefixes,omitempty"`
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
