package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The collector's Secret and ConfigMap are rewritten on every run against a
// namespace a previous run may have left behind. Two things have to hold: the
// payload is replaced outright, so no key from an earlier run survives to be
// mounted and read, and the metadata the server owns - resourceVersion and
// whatever labels, annotations or owner references were attached to the live
// object - is carried over rather than dropped.
//
// These are fake-client tests: they prove the payload and control flow the
// code produces, not how a real API server resolves an update. Omitted-key
// removal is asserted on the object this code sends, which is where the
// decision is made.

const (
	applyNamespace = "flightctl-catalog-collector-e2e"
	applySecret    = "collector-credentials"
	applyConfigMap = "collector-config"
)

// livingObjectMeta is the metadata a server-side object carries that a
// freshly constructed one does not.
func livingObjectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:            name,
		Namespace:       applyNamespace,
		ResourceVersion: "4711",
		UID:             "7f1d1c2e-0000-4000-8000-00000000abcd",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "Helm",
			"stale-run":                    "previous",
		},
		Annotations: map[string]string{
			"meta.helm.sh/release-name": "flightctl-catalog-collector",
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Name:       "owner",
			UID:        "0f0f0f0f-0000-4000-8000-00000000ffff",
		}},
		Finalizers: []string{"example.com/keep"},
	}
}

func requireMetadataPreserved(t *testing.T, want, got metav1.ObjectMeta) {
	t.Helper()
	require.Equal(t, want.ResourceVersion, got.ResourceVersion,
		"the update must carry the resourceVersion read from the server, so it is conflict-checked")
	require.Equal(t, want.UID, got.UID)
	require.Equal(t, want.Labels, got.Labels, "labels on the live object must survive a payload replacement")
	require.Equal(t, want.Annotations, got.Annotations)
	require.Equal(t, want.OwnerReferences, got.OwnerReferences)
	require.Equal(t, want.Finalizers, got.Finalizers)
}

// captureSecretUpdate records the Secret handed to Update, which is the object
// whose payload the server would apply.
func captureSecretUpdate(client *fake.Clientset, into **corev1.Secret) {
	client.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		*into = obj.DeepCopy()
		return false, nil, nil
	})
}

func captureConfigMapUpdate(client *fake.Clientset, into **corev1.ConfigMap) {
	client.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		*into = obj.DeepCopy()
		return false, nil, nil
	})
}

func TestApplySecretRejectsIncompleteCoordinates(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		object    string
	}{
		{name: "When the namespace is empty, it should fail", namespace: "", object: applySecret},
		{name: "When the namespace is blank, it should fail", namespace: "   ", object: applySecret},
		{name: "When the name is empty, it should fail", namespace: applyNamespace, object: ""},
		{name: "When the name is blank, it should fail", namespace: applyNamespace, object: "\t"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := &clusterRuntime{client: fake.NewSimpleClientset()}
			require.Error(t, rt.applySecret(context.Background(), test.namespace, test.object, map[string]string{"k": "v"}))
			require.Error(t, rt.applyConfigMap(context.Background(), test.namespace, test.object, map[string]string{"k": "v"}))
		})
	}
}

func TestApplySecretCreatesWhenAbsent(t *testing.T) {
	client := fake.NewSimpleClientset()
	rt := &clusterRuntime{client: client}

	require.NoError(t, rt.applySecret(context.Background(), applyNamespace, applySecret,
		map[string]string{"client-secret": "s3cret", "ca.crt": "PEM"}))

	// One round trip: an absent object must not cost a read. Checked before
	// the assertions below, which issue a read of their own.
	require.Equal(t, []string{"create"}, verbsFor(client, "secrets"))

	created, err := client.CoreV1().Secrets(applyNamespace).Get(context.Background(), applySecret, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, corev1.SecretTypeOpaque, created.Type)
	require.Equal(t, map[string]string{"client-secret": "s3cret", "ca.crt": "PEM"}, created.StringData)
}

func TestApplyConfigMapCreatesWhenAbsent(t *testing.T) {
	client := fake.NewSimpleClientset()
	rt := &clusterRuntime{client: client}

	require.NoError(t, rt.applyConfigMap(context.Background(), applyNamespace, applyConfigMap,
		map[string]string{"config.yaml": "sources: []"}))

	require.Equal(t, []string{"create"}, verbsFor(client, "configmaps"))

	created, err := client.CoreV1().ConfigMaps(applyNamespace).Get(context.Background(), applyConfigMap, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"config.yaml": "sources: []"}, created.Data)
}

func TestApplySecretReplacesPayloadAndKeepsMetadata(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: livingObjectMeta(applySecret),
		Type:       corev1.SecretTypeOpaque,
		// Decoded keys the server produced from a previous StringData write.
		Data: map[string][]byte{
			"client-secret": []byte("old"),
			"obsolete-key":  []byte("left over from an earlier run"),
		},
	}
	client := fake.NewSimpleClientset(existing)
	var sent *corev1.Secret
	captureSecretUpdate(client, &sent)
	rt := &clusterRuntime{client: client}

	require.NoError(t, rt.applySecret(context.Background(), applyNamespace, applySecret,
		map[string]string{"client-secret": "new"}))

	require.NotNil(t, sent, "the existing object must be updated, not recreated")
	require.Equal(t, map[string]string{"client-secret": "new"}, sent.StringData)
	require.Nil(t, sent.Data,
		"the decoded keys from the previous object must be cleared, or obsolete-key stays mounted")
	requireMetadataPreserved(t, existing.ObjectMeta, sent.ObjectMeta)
	require.Equal(t, []string{"create", "get", "update"}, verbsFor(client, "secrets"),
		"the update must be built from a fresh read")
}

func TestApplyConfigMapReplacesPayloadAndKeepsMetadata(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: livingObjectMeta(applyConfigMap),
		Data: map[string]string{
			"config.yaml": "sources: [old]",
			"obsolete":    "left over from an earlier run",
		},
		BinaryData: map[string][]byte{
			"truststore.jks": {0x01, 0x02},
		},
	}
	client := fake.NewSimpleClientset(existing)
	var sent *corev1.ConfigMap
	captureConfigMapUpdate(client, &sent)
	rt := &clusterRuntime{client: client}

	require.NoError(t, rt.applyConfigMap(context.Background(), applyNamespace, applyConfigMap,
		map[string]string{"config.yaml": "sources: [new]"}))

	require.NotNil(t, sent)
	require.Equal(t, map[string]string{"config.yaml": "sources: [new]"}, sent.Data)
	require.Nil(t, sent.BinaryData,
		"a binary key this call did not ask for must not survive the replacement")
	requireMetadataPreserved(t, existing.ObjectMeta, sent.ObjectMeta)
	require.Equal(t, []string{"create", "get", "update"}, verbsFor(client, "configmaps"))
}

func TestApplyReportsCreateFailures(t *testing.T) {
	createFailure := errors.New("namespace is terminating")

	t.Run("When creating the secret fails for a reason other than conflict, it should surface it", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, createFailure
		})
		rt := &clusterRuntime{client: client}
		err := rt.applySecret(context.Background(), applyNamespace, applySecret, map[string]string{"k": "v"})
		require.ErrorIs(t, err, createFailure)
		require.NotContains(t, verbsFor(client, "secrets"), "update",
			"a create failure that is not AlreadyExists must not fall through to an update")
	})

	t.Run("When creating the configmap fails for a reason other than conflict, it should surface it", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, createFailure
		})
		rt := &clusterRuntime{client: client}
		err := rt.applyConfigMap(context.Background(), applyNamespace, applyConfigMap, map[string]string{"k": "v"})
		require.ErrorIs(t, err, createFailure)
		require.NotContains(t, verbsFor(client, "configmaps"), "update")
	})
}

func TestApplyReportsReadFailureAfterAlreadyExists(t *testing.T) {
	// AlreadyExists followed by a failing read must be reported, not papered
	// over by sending a blind update built from nothing.
	readFailure := errors.New("etcd unavailable")

	t.Run("When the secret cannot be read back, it should fail without updating", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, applySecret)
		})
		client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, readFailure
		})
		rt := &clusterRuntime{client: client}
		err := rt.applySecret(context.Background(), applyNamespace, applySecret, map[string]string{"k": "v"})
		require.ErrorIs(t, err, readFailure)
		require.NotContains(t, verbsFor(client, "secrets"), "update")
	})

	t.Run("When the configmap cannot be read back, it should fail without updating", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, applyConfigMap)
		})
		client.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, readFailure
		})
		rt := &clusterRuntime{client: client}
		err := rt.applyConfigMap(context.Background(), applyNamespace, applyConfigMap, map[string]string{"k": "v"})
		require.ErrorIs(t, err, readFailure)
		require.NotContains(t, verbsFor(client, "configmaps"), "update")
	})
}

func TestApplyReportsUpdateConflict(t *testing.T) {
	// Another writer touched the object between the read and the write. The
	// conflict is the point of sending the resourceVersion, so it has to be
	// reported rather than retried silently.
	t.Run("When the secret update conflicts, it should surface the conflict", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: livingObjectMeta(applySecret)})
		client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: "secrets"}, applySecret, errors.New("resourceVersion changed"))
		})
		rt := &clusterRuntime{client: client}
		err := rt.applySecret(context.Background(), applyNamespace, applySecret, map[string]string{"k": "v"})
		require.Error(t, err)
		require.True(t, apierrors.IsConflict(errors.Unwrap(err)), "the conflict must stay recognisable: %v", err)
	})

	t.Run("When the configmap update conflicts, it should surface the conflict", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: livingObjectMeta(applyConfigMap)})
		client.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: "configmaps"}, applyConfigMap, errors.New("resourceVersion changed"))
		})
		rt := &clusterRuntime{client: client}
		err := rt.applyConfigMap(context.Background(), applyNamespace, applyConfigMap, map[string]string{"k": "v"})
		require.Error(t, err)
		require.True(t, apierrors.IsConflict(errors.Unwrap(err)), "the conflict must stay recognisable: %v", err)
	})
}

// verbsFor lists the verbs the client recorded against one resource, in order.
func verbsFor(client *fake.Clientset, resource string) []string {
	var verbs []string
	for _, action := range client.Actions() {
		if action.GetResource().Resource == resource {
			verbs = append(verbs, action.GetVerb())
		}
	}
	return verbs
}
