package trustifyv2

import "time"

// Finding represents a single vulnerability finding for an image digest,
// matching the vulnerability_findings table schema.  This is our domain type;
// it is not generated and stays here permanently.
type Finding struct {
	ImageDigest string
	CVEID       string
	Status      string
	Severity    string
	AdvisoryID  string
	Issuer      *Issuer
	CVSSScore   *float64
	Description string
	PublishedAt *time.Time
}
