package firmwarepolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
)

const Version = "firmware-policy/v1"
const MaxDocumentBytes = 8 * 1024 * 1024
const MaxWireBytes = ((MaxDocumentBytes+2)/3)*4 + 512

var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Binding struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	DescriptorDigest string `json:"descriptorDigest"`
	Descriptor       []byte `json:"descriptor"`
}

type Tombstone struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	DescriptorDigest string `json:"descriptorDigest"`
	Reason           string `json:"reason"`
	Revision         int64  `json:"revision"`
}

type Document struct {
	APIVersion string      `json:"apiVersion"`
	Revision   int64       `json:"revision"`
	Bindings   []Binding   `json:"bindings"`
	Tombstones []Tombstone `json:"tombstones"`
}

type Receipt struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

// View binds exact canonical document bytes to the accepted revision.
type View struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Document []byte `json:"document"`
}

func Hash(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

func New(revision int64, bindings []Binding, tombstones []Tombstone) (View, error) {
	d := Document{APIVersion: Version, Revision: revision, Bindings: append([]Binding{}, bindings...), Tombstones: append([]Tombstone{}, tombstones...)}
	slices.SortFunc(d.Bindings, func(a, b Binding) int { return compare(a.Name, a.Version, b.Name, b.Version) })
	slices.SortFunc(d.Tombstones, func(a, b Tombstone) int { return compare(a.Name, a.Version, b.Name, b.Version) })
	if err := validate(d); err != nil {
		return View{}, err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return View{}, err
	}
	if len(data) > MaxDocumentBytes {
		return View{}, errors.New("policy document exceeds limit")
	}
	return View{Revision: revision, Digest: Hash(data), Document: data}, nil
}

func compare(an, av, bn, bv string) int {
	if n := bytes.Compare([]byte(an), []byte(bn)); n != 0 {
		return n
	}
	return bytes.Compare([]byte(av), []byte(bv))
}
func key(name, version string) string { return name + "/" + version }

func Decode(view View) (Document, error) {
	var d Document
	if len(view.Document) == 0 || len(view.Document) > MaxDocumentBytes || view.Revision <= 0 || !digestPattern.MatchString(view.Digest) || Hash(view.Document) != view.Digest {
		return d, errors.New("invalid policy envelope or digest")
	}
	if err := uniqueJSON(view.Document); err != nil {
		return d, err
	}
	dec := json.NewDecoder(bytes.NewReader(view.Document))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Document{}, err
	}
	if d.Revision != view.Revision {
		return Document{}, errors.New("policy revision differs from document")
	}
	if err := validate(d); err != nil {
		return Document{}, err
	}
	canonical, err := New(d.Revision, d.Bindings, d.Tombstones)
	if err != nil {
		return Document{}, err
	}
	if !bytes.Equal(canonical.Document, view.Document) {
		return Document{}, errors.New("policy document is not canonical")
	}
	return d, nil
}

func validate(d Document) error {
	if d.APIVersion != Version || d.Revision <= 0 || len(d.Bindings) > 256 || len(d.Tombstones) > 256 {
		return errors.New("unsupported policy version, revision or record count")
	}
	bindings := map[string]Binding{}
	total := 0
	for _, b := range d.Bindings {
		k := key(b.Name, b.Version)
		if !identityPattern.MatchString(b.Name) || !identityPattern.MatchString(b.Version) || !digestPattern.MatchString(b.DescriptorDigest) || Hash(b.Descriptor) != b.DescriptorDigest {
			return errors.New("invalid policy binding identity")
		}
		if _, ok := bindings[k]; ok {
			return errors.New("duplicate policy binding")
		}
		total += len(b.Descriptor)
		if total > 4*1024*1024 {
			return errors.New("retained policy descriptors exceed limit")
		}
		if err := uniqueJSON(b.Descriptor); err != nil {
			return err
		}
		var identity struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(b.Descriptor, &identity); err != nil {
			return err
		}
		if identity.Name != b.Name || identity.Version != b.Version {
			return errors.New("descriptor identity differs from policy binding")
		}
		bindings[k] = b
	}
	tombstones := map[string]bool{}
	for _, t := range d.Tombstones {
		k := key(t.Name, t.Version)
		b, ok := bindings[k]
		if !ok || tombstones[k] || t.DescriptorDigest != b.DescriptorDigest || t.Revision <= 0 || t.Revision > d.Revision || len(t.Reason) == 0 || len(t.Reason) > 1024 {
			return errors.New("invalid retained policy tombstone")
		}
		tombstones[k] = true
	}
	return nil
}

// ValidateTransition checks history independently of current target or attempt state.
func ValidateTransition(previous *View, next View) (bool, error) {
	nd, err := Decode(next)
	if err != nil {
		return false, err
	}
	if previous == nil {
		return true, nil
	}
	pd, err := Decode(*previous)
	if err != nil {
		return false, fmt.Errorf("retained policy unavailable: %w", err)
	}
	if next.Revision < previous.Revision {
		return false, errors.New("stale policy revision")
	}
	if next.Revision == previous.Revision {
		if next.Digest != previous.Digest {
			return false, errors.New("conflicting policy revision")
		}
		return false, nil
	}
	bindings := map[string]Binding{}
	for _, b := range nd.Bindings {
		bindings[key(b.Name, b.Version)] = b
	}
	for _, old := range pd.Bindings {
		b, ok := bindings[key(old.Name, old.Version)]
		if !ok || b.DescriptorDigest != old.DescriptorDigest || !bytes.Equal(b.Descriptor, old.Descriptor) {
			return false, errors.New("retained binding was removed or retargeted")
		}
	}
	tombstones := map[string]Tombstone{}
	for _, t := range nd.Tombstones {
		tombstones[key(t.Name, t.Version)] = t
	}
	previousTombstones := map[string]bool{}
	for _, old := range pd.Tombstones {
		previousTombstones[key(old.Name, old.Version)] = true
	}
	for _, next := range nd.Tombstones {
		if !previousTombstones[key(next.Name, next.Version)] && next.Revision <= pd.Revision {
			return false, errors.New("new revocation predates the retained policy")
		}
	}
	for _, old := range pd.Tombstones {
		t, ok := tombstones[key(old.Name, old.Version)]
		if !ok || t != old {
			return false, errors.New("retained revocation was removed or changed")
		}
	}
	return true, nil
}

func uniqueJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanJSON(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("policy JSON contains trailing content")
	}
	return nil
}

func scanJSON(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("policy JSON exceeds depth limit")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			k, ok := t.(string)
			if !ok || seen[k] {
				return errors.New("duplicate policy JSON key")
			}
			seen[k] = true
			if err := scanJSON(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanJSON(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid policy JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

func (r Receipt) Validate() error {
	if r.Revision <= 0 || !digestPattern.MatchString(r.Digest) {
		return errors.New("invalid policy receipt identity")
	}
	return nil
}

func ParseView(data []byte) (View, error) {
	if len(data) > MaxWireBytes {
		return View{}, errors.New("policy response exceeds limit")
	}
	if err := uniqueJSON(data); err != nil {
		return View{}, err
	}
	var view View
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&view); err != nil {
		return View{}, err
	}
	if _, err := Decode(view); err != nil {
		return View{}, err
	}
	return view, nil
}

func ParseReceipt(data []byte) (Receipt, error) {
	if len(data) > 4096 {
		return Receipt{}, errors.New("policy receipt exceeds limit")
	}
	if err := uniqueJSON(data); err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, receipt.Validate()
}
