// Package quay provides a client for the Quay Security API, which exposes
// Clair vulnerability scan results for images hosted on a Quay instance.
package quay

// Response is the top-level payload returned by the Quay Security API
// (GET /api/v1/repository/{namespace}/{repo}/manifest/{digest}/security).
// Status reports the scan state; only "scanned" carries vulnerability data.
type Response struct {
	Status string `json:"status"`
	Data   *Data  `json:"data,omitempty"`
}

// Data wraps the scanned layer of a manifest.
type Data struct {
	Layer *Layer `json:"Layer,omitempty"`
}

// Layer holds the features (installed packages) detected in a scanned image.
type Layer struct {
	Name             string    `json:"Name,omitempty"`
	ParentName       string    `json:"ParentName,omitempty"`
	NamespaceName    string    `json:"NamespaceName,omitempty"`
	IndexedByVersion int       `json:"IndexedByVersion,omitempty"`
	Features         []Feature `json:"Features,omitempty"`
}

// Feature is an installed package and the vulnerabilities affecting it.
type Feature struct {
	Name            string          `json:"Name,omitempty"`
	VersionFormat   string          `json:"VersionFormat,omitempty"`
	NamespaceName   string          `json:"NamespaceName,omitempty"`
	AddedBy         string          `json:"AddedBy,omitempty"`
	Version         string          `json:"Version,omitempty"`
	Vulnerabilities []Vulnerability `json:"Vulnerabilities,omitempty"`
}

// Vulnerability describes a single vulnerability reported against a Feature.
type Vulnerability struct {
	Name          string   `json:"Name,omitempty"`
	NamespaceName string   `json:"NamespaceName,omitempty"`
	Description   string   `json:"Description,omitempty"`
	Link          string   `json:"Link,omitempty"`
	Severity      string   `json:"Severity,omitempty"`
	FixedBy       string   `json:"FixedBy,omitempty"`
	Metadata      Metadata `json:"Metadata,omitempty"`
}

// Metadata carries the NVD enrichment Quay attaches to a vulnerability.
type Metadata struct {
	NVD NVD `json:"NVD,omitempty"`
}

// NVD holds the CVSS scores from the National Vulnerability Database.
type NVD struct {
	CVSSv3 CVSS `json:"CVSSv3,omitempty"`
	CVSSv2 CVSS `json:"CVSSv2,omitempty"`
}

// CVSS is a single CVSS scoring entry.
type CVSS struct {
	Vectors string  `json:"Vectors,omitempty"`
	Score   float64 `json:"Score,omitempty"`
}
