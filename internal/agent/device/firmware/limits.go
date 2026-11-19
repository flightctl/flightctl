package firmware

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"reflect"
	"slices"
	"sort"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
)

const (
	// MaxPayloadDepth is the largest number of components in a manifest file
	// path, counting the leading payload component.
	MaxPayloadDepth = 16
	// maxPayloadFiles is the largest file count any extraction budget admits.
	maxPayloadFiles = 4096
)

// DefaultExtractionCeilings returns the largest extraction budgets the agent
// accepts from a release. A release declaring more is rejected, not clamped.
func DefaultExtractionCeilings() policy.ExtractionLimits {
	return policy.ExtractionLimits{
		MaxExtractedManifestBytes: 1 << 20,
		MaxExtractedFiles:         maxPayloadFiles,
		MaxExtractedFileBytes:     1 << 30,
		MaxExtractedTotalBytes:    2 << 30,
		ExtractionScratchBytes:    3 << 30,
		MaxExtractionSeconds:      1800,
	}
}

// boundExtraction is the extraction budget taken from the authenticated
// release descriptor that a fetch verified, together with the identity of the
// package that fetch authenticated.
type boundExtraction struct {
	descriptorDigest string
	limits           *policy.ExtractionLimits
	budgetKeys       []string
	identity         *packageIdentity
}

// packageIdentity is the identity of a verified package as its fetch
// authenticated it.
type packageIdentity struct {
	name, version, packageDigest, reference string
	publisherPolicyDigest, manifestDigest   string
	manifest                                Manifest
}

// bindExtraction binds the extraction budget of d to the identity that pkg
// carries when its fetch has authenticated it.
func bindExtraction(pkg *VerifiedPackage, d *policy.ReleaseDescriptor) boundExtraction {
	b := boundExtraction{descriptorDigest: pkg.DescriptorDigest, budgetKeys: slices.Clone(d.BudgetKeySet)}
	if d.Extraction != nil {
		limits := *d.Extraction
		b.limits = &limits
	}
	if manifest, err := cloneManifest(pkg.Manifest); err == nil {
		b.identity = &packageIdentity{
			name: pkg.Name, version: pkg.Version, packageDigest: pkg.PackageDigest, reference: pkg.Reference,
			publisherPolicyDigest: pkg.PublisherPolicyDigest, manifestDigest: pkg.ManifestDigest, manifest: manifest,
		}
	}
	return b
}

// cloneManifest returns a deep copy of m.
func cloneManifest(m Manifest) (Manifest, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return Manifest{}, err
	}
	var c Manifest
	err = json.Unmarshal(data, &c)
	return c, err
}

// matches checks that pkg still carries the identity its fetch authenticated.
func (b boundExtraction) matches(pkg *VerifiedPackage) error {
	id := b.identity
	switch {
	case b.descriptorDigest != pkg.DescriptorDigest:
		return fmt.Errorf("extraction budget is bound to descriptor %s, not %s", b.descriptorDigest, pkg.DescriptorDigest)
	case id == nil:
		return errors.New("extraction budget is not bound to an authenticated package")
	case pkg.Name != id.name || pkg.Version != id.version || pkg.PackageDigest != id.packageDigest || pkg.Reference != id.reference ||
		pkg.PublisherPolicyDigest != id.publisherPolicyDigest || pkg.ManifestDigest != id.manifestDigest:
		return errors.New("package identity differs from the one its fetch authenticated")
	case !reflect.DeepEqual(pkg.Manifest, id.manifest):
		return errors.New("package manifest differs from the one its fetch authenticated")
	}
	return nil
}

// ExtractionLimits returns the extraction budget of the authenticated release
// descriptor, and false when the release declares none.
func (p *VerifiedPackage) ExtractionLimits() (policy.ExtractionLimits, bool) {
	if p == nil || p.extraction.limits == nil {
		return policy.ExtractionLimits{}, false
	}
	return *p.extraction.limits, true
}

// EffectiveExtractionLimits returns the authenticated extraction budget of
// pkg after checking that pkg still carries the complete identity its fetch
// authenticated and that the budget names only listed budget keys and lies
// within ceilings.
func EffectiveExtractionLimits(pkg *VerifiedPackage, ceilings policy.ExtractionLimits) (policy.ExtractionLimits, error) {
	if pkg == nil {
		return policy.ExtractionLimits{}, unavailable("", false, errors.New("no verified package"))
	}
	release := pkg.Name + "/" + pkg.Version
	b := pkg.extraction
	if b.limits == nil {
		return policy.ExtractionLimits{}, unavailable(release, false, ErrNoExtractionBudget)
	}
	if err := b.matches(pkg); err != nil {
		return policy.ExtractionLimits{}, unavailable(release, false, err)
	}
	limits := *b.limits
	if err := limits.Validate(); err != nil {
		return policy.ExtractionLimits{}, unavailable(release, false, err)
	}
	values, maxima := limits.Values(), ceilings.Values()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !slices.Contains(b.budgetKeys, key) {
			return policy.ExtractionLimits{}, unavailable(release, false, fmt.Errorf("extraction budget %s is not a release budget key", key))
		}
	}
	for _, key := range keys {
		if values[key] > maxima[key] {
			return policy.ExtractionLimits{}, notSupported(release, fmt.Errorf("extraction budget %s=%d exceeds the agent ceiling %d", key, values[key], maxima[key]))
		}
	}
	return limits, nil
}

// MaxTreeNodes returns the largest number of nodes a published payload tree
// can hold: its root, manifest.json and payload, plus at most depth-1 nodes
// per file for paths of depth components counting payload.
func MaxTreeNodes(files, depth int) (int, error) {
	if files < 1 || files > maxPayloadFiles || depth < 2 || depth > MaxPayloadDepth {
		return 0, fmt.Errorf("payload tree of %d files at depth %d is outside the supported bounds", files, depth)
	}
	hi, perFile := bits.Mul64(uint64(depth-1), uint64(files))
	total, carry := bits.Add64(perFile, 3, 0)
	if hi != 0 || carry != 0 || total > uint64(maxInt) {
		return 0, fmt.Errorf("payload tree of %d files at depth %d overflows", files, depth)
	}
	return int(total), nil
}

const maxInt = int(^uint(0) >> 1)
