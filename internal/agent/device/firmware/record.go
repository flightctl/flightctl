package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
)

const (
	recordAPIVersion = "updater-package-record/v1"
	maxRecordBytes   = 2 * MaxManifestBytes
)

// packageRecord durably identifies a verified updater package. It is written
// only after every check passed and is reused only while it matches the
// approved descriptor and the image is still present.
type packageRecord struct {
	APIVersion            string   `json:"apiVersion"`
	Name                  string   `json:"name"`
	Version               string   `json:"version"`
	DescriptorDigest      string   `json:"descriptorDigest"`
	PackageDigest         string   `json:"packageDigest"`
	PublisherPolicyDigest string   `json:"publisherPolicyDigest"`
	ManifestDigest        string   `json:"manifestDigest"`
	PolicyRevision        int64    `json:"policyRevision"`
	Manifest              Manifest `json:"manifest"`
}

func (f *Fetcher) recordPath(descriptorDigest string) string {
	return filepath.Join(f.dataDir, "firmware", "packages", digestHex(descriptorDigest)+".json")
}

func (f *Fetcher) writeRecord(pkg *VerifiedPackage) error {
	data, err := json.Marshal(packageRecord{
		APIVersion:            recordAPIVersion,
		Name:                  pkg.Name,
		Version:               pkg.Version,
		DescriptorDigest:      pkg.DescriptorDigest,
		PackageDigest:         pkg.PackageDigest,
		PublisherPolicyDigest: pkg.PublisherPolicyDigest,
		ManifestDigest:        pkg.ManifestDigest,
		PolicyRevision:        pkg.PolicyRevision,
		Manifest:              pkg.Manifest,
	})
	if err != nil {
		return err
	}
	return f.rw.WriteFileDurable(f.recordPath(pkg.DescriptorDigest), data, 0o700, 0o600)
}

func (f *Fetcher) readRecord(descriptorDigest string) (*packageRecord, error) {
	data, err := f.rw.ReadFileLimited(f.recordPath(descriptorDigest), maxRecordBytes)
	if err != nil {
		return nil, err
	}
	var record packageRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode verified record: %w", err)
	}
	return &record, nil
}

// matches reports whether the record identifies the package d approves.
func (r *packageRecord) matches(descriptorDigest string, d *policy.ReleaseDescriptor) error {
	switch {
	case r.APIVersion != recordAPIVersion:
		return fmt.Errorf("unsupported verified record version %q", r.APIVersion)
	case r.Name != d.Name || r.Version != d.Version || r.DescriptorDigest != descriptorDigest:
		return errors.New("verified record names another release descriptor")
	case r.PackageDigest != d.PackageDigest || r.PublisherPolicyDigest != d.Publisher.PolicyDigest:
		return errors.New("verified record names another package or publisher policy")
	case !schemaDigestPattern.MatchString(r.ManifestDigest):
		return errors.New("verified record manifest digest is malformed")
	}
	return VerifyManifestBinding(&r.Manifest, d)
}

// cachedPackage returns the recorded package when its record matches the
// approved descriptor and the image is still present by digest. Any other
// record state leads to a full fetch.
func (f *Fetcher) cachedPackage(ctx context.Context, revision int64, descriptorDigest string, d *policy.ReleaseDescriptor) (*VerifiedPackage, bool) {
	release := d.Name + "/" + d.Version
	record, err := f.readRecord(descriptorDigest)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			f.log.Warnf("Ignoring the verified record for updater %s: %v", release, err)
		}
		return nil, false
	}
	if err := record.matches(descriptorDigest, d); err != nil {
		f.log.Warnf("Ignoring the verified record for updater %s: %v", release, err)
		return nil, false
	}
	reference := d.PackageReference()
	if !f.podman.ImageExists(ctx, reference) {
		f.log.Infof("Updater %s image %s is no longer present; fetching it again", release, reference)
		return nil, false
	}
	return &VerifiedPackage{
		Name:                  record.Name,
		Version:               record.Version,
		DescriptorDigest:      record.DescriptorDigest,
		PackageDigest:         record.PackageDigest,
		Reference:             reference,
		PublisherPolicyDigest: record.PublisherPolicyDigest,
		ManifestDigest:        record.ManifestDigest,
		PolicyRevision:        revision,
		Manifest:              record.Manifest,
		Cached:                true,
	}, true
}
