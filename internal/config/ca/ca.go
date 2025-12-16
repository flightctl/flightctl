package ca

import (
	api "github.com/flightctl/flightctl/api/v1beta1"
)

type CAIdType int

const (
	InternalCA CAIdType = iota + 1
	AsyncInternalCA
)

type InternalCfg struct {
	CertFile         string `json:"certFile,omitempty"`
	KeyFile          string `json:"keyFile,omitempty"`
	CABundleFile     string `json:"caBundleFile,omitempty"`
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
	DeviceEnrollmentSignerName      string       `json:"deviceEnrollmentSignerName,omitempty"`
	ClientBootstrapCommonNamePrefix string       `json:"clientBootstrapCommonNamePrefix,omitempty"`
	DeviceManagementSignerName      string       `json:"deviceManagementSignerName,omitempty"`
	DeviceSvcClientSignerName       string       `json:"deviceSvcClientSignerName,omitempty"`
	ServerSvcSignerName             string       `json:"serverSvcSignerName,omitempty"`
	ClientBootstrapValidityDays     int          `json:"clientBootStrapValidityDays,omitempty"`
	DeviceCommonNamePrefix          string       `json:"deviceCommonNamePrefix,omitempty"`
	InternalConfig                  *InternalCfg `json:"internalConfig,omitempty"`
	ServerCertValidityDays          int          `json:"serverCertValidityDays,omitempty"`
	ExtraAllowedPrefixes            []string     `json:"extraAllowedPrefixes,omitempty"`
}

func NewDefault(tempDir string) *Config {
	c := &Config{
		CAType:                          InternalCA,
		AdminCommonName:                 api.ExternalRoleAdmin,
		ClientBootstrapCertName:         "client-enrollment",
		ClientBootstrapCommonName:       "client-enrollment",
		DeviceEnrollmentSignerName:      "flightctl.io/enrollment",
		ClientBootstrapCommonNamePrefix: "client-enrollment-",
		DeviceManagementSignerName:      "flightctl.io/device-enrollment",
		DeviceSvcClientSignerName:       "flightctl.io/device-svc-client",
		ServerSvcSignerName:             "flightctl.io/server-svc",
		ClientBootstrapValidityDays:     365,
		ServerCertValidityDays:          365,
		DeviceCommonNamePrefix:          "device:",
		InternalConfig: &InternalCfg{
			CertFile:         "client-signer.crt",
			KeyFile:          "client-signer.key",
			CABundleFile:     "ca-bundle.crt",
			CertValidityDays: 3650,
			SignerCertName:   "client-signer",
			CertStore:        tempDir,
		},
	}
	return c
}
