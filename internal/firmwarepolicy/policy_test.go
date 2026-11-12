package firmwarepolicy_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
)

func binding(t *testing.T) firmwarepolicy.Binding {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	data, err := json.Marshal(cfg.Releases[0])
	require.NoError(t, err)
	return firmwarepolicy.Binding{Name: cfg.Releases[0].Name, Version: cfg.Releases[0].Version, DescriptorDigest: firmwarepolicy.Hash(data), Descriptor: data}
}

func TestPolicyRoundTrip(t *testing.T) {
	b := binding(t)
	view, err := firmwarepolicy.New(1, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	decoded, err := firmwarepolicy.Decode(view)
	require.NoError(t, err)
	require.Equal(t, int64(1), decoded.Revision)
	require.Equal(t, b, decoded.Bindings[0])
	replay, err := firmwarepolicy.New(1, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	require.Equal(t, view, replay)
	changed, err := firmwarepolicy.ValidateTransition(&view, replay)
	require.NoError(t, err)
	require.False(t, changed)
	view.Document[0] = 'x'
	_, err = firmwarepolicy.Decode(view)
	require.Error(t, err)
}

func TestPolicyRetainedHistory(t *testing.T) {
	b := binding(t)
	one, err := firmwarepolicy.New(1, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	tomb := firmwarepolicy.Tombstone{Name: b.Name, Version: b.Version, DescriptorDigest: b.DescriptorDigest, Reason: "revoked", Revision: 2}
	two, err := firmwarepolicy.New(2, []firmwarepolicy.Binding{b}, []firmwarepolicy.Tombstone{tomb})
	require.NoError(t, err)
	changed, err := firmwarepolicy.ValidateTransition(&one, two)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = firmwarepolicy.ValidateTransition(&two, one)
	require.Error(t, err)
	three, err := firmwarepolicy.New(3, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	_, err = firmwarepolicy.ValidateTransition(&two, three)
	require.Error(t, err)
	missing, err := firmwarepolicy.New(3, nil, nil)
	require.NoError(t, err)
	_, err = firmwarepolicy.ValidateTransition(&two, missing)
	require.Error(t, err)
	tomb.Reason = "rewritten"
	rewritten, err := firmwarepolicy.New(3, []firmwarepolicy.Binding{b}, []firmwarepolicy.Tombstone{tomb})
	require.NoError(t, err)
	_, err = firmwarepolicy.ValidateTransition(&two, rewritten)
	require.Error(t, err)
	var descriptor map[string]any
	require.NoError(t, json.Unmarshal(b.Descriptor, &descriptor))
	descriptor["protocol"] = 2
	b.Descriptor, err = json.Marshal(descriptor)
	require.NoError(t, err)
	b.DescriptorDigest = firmwarepolicy.Hash(b.Descriptor)
	retarget, err := firmwarepolicy.New(3, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	_, err = firmwarepolicy.ValidateTransition(&one, retarget)
	require.Error(t, err)
	conflicting, err := firmwarepolicy.New(1, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	_, err = firmwarepolicy.ValidateTransition(&one, conflicting)
	require.Error(t, err)
}

func TestPolicyValidation(t *testing.T) {
	b := binding(t)
	for _, tc := range []struct {
		name     string
		revision int64
		bindings []firmwarepolicy.Binding
		tombs    []firmwarepolicy.Tombstone
	}{
		{"When revision is zero it should reject", 0, []firmwarepolicy.Binding{b}, nil},
		{"When a binding repeats it should reject", 1, []firmwarepolicy.Binding{b, b}, nil},
		{"When a tombstone lacks evidence it should reject", 1, nil, []firmwarepolicy.Tombstone{{Name: b.Name, Version: b.Version, DescriptorDigest: b.DescriptorDigest, Revision: 1, Reason: "revoked"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := firmwarepolicy.New(tc.revision, tc.bindings, tc.tombs)
			require.Error(t, err)
		})
	}
	valid, err := firmwarepolicy.New(1, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	for _, document := range []string{
		`{"apiVersion":"firmware-policy/v1","revision":1,"revision":2,"bindings":[],"tombstones":[]}`,
		`{"apiVersion":"firmware-policy/v2","revision":1,"bindings":[],"tombstones":[]}`,
		`{"apiVersion":"firmware-policy/v1","revision":1,"bindings":[],"tombstones":[],"unknown":true}`,
	} {
		view := valid
		view.Document = []byte(document)
		view.Digest = firmwarepolicy.Hash(view.Document)
		_, err := firmwarepolicy.Decode(view)
		require.Error(t, err)
	}
}

func TestPolicyCanonicalEncoding(t *testing.T) {
	view, err := firmwarepolicy.New(1, nil, nil)
	require.NoError(t, err)
	expected := `{"apiVersion":"firmware-policy/v1","revision":1,"bindings":[],"tombstones":[]}`
	require.Equal(t, expected, string(view.Document))
	require.Equal(t, firmwarepolicy.Hash([]byte(expected)), view.Digest)
	var doc firmwarepolicy.Document
	require.NoError(t, json.Unmarshal(view.Document, &doc))
	view.Document, err = json.MarshalIndent(doc, "", " ")
	require.NoError(t, err)
	view.Digest = firmwarepolicy.Hash(view.Document)
	_, err = firmwarepolicy.Decode(view)
	require.Error(t, err)
}

func TestPolicyNewRevocationCannotBeBackdated(t *testing.T) {
	b := binding(t)
	before, err := firmwarepolicy.New(2, []firmwarepolicy.Binding{b}, nil)
	require.NoError(t, err)
	next, err := firmwarepolicy.New(3, []firmwarepolicy.Binding{b}, []firmwarepolicy.Tombstone{{Name: b.Name, Version: b.Version, DescriptorDigest: b.DescriptorDigest, Reason: "revoked", Revision: 1}})
	require.NoError(t, err)
	_, err = firmwarepolicy.ValidateTransition(&before, next)
	require.Error(t, err)
}
