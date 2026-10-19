package k8s

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// These cover waitForWorkloadReady against a fake Kubernetes API rather than
// through a stubbed instance, because the logic under test is the readiness
// decision itself: which pods count, what "ready" means, and which outcomes
// are failures rather than "not yet".

const (
	readyTestNamespace = "flightctl-e2e-cc-abc123"
	readyTestSelector  = "app.kubernetes.io/name=flightctl-catalog-collector,app.kubernetes.io/instance=e2e-catalog-collector-abc123"
)

// collectorPod builds a pod carrying the labels the chart renders, with the
// given Ready condition.
func collectorPod(name string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: readyTestNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":     collectorChartName,
				"app.kubernetes.io/instance": "e2e-catalog-collector-abc123",
			},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

// otherPod is a ready pod belonging to something else in the same namespace.
// It must never satisfy the collector's readiness wait.
func otherPod(name string) *corev1.Pod {
	pod := collectorPod(name, true)
	pod.Labels["app.kubernetes.io/name"] = "some-other-workload"
	return pod
}

func TestWaitForWorkloadReady(t *testing.T) {
	tests := []struct {
		name string
		// objects are the pods the API already holds.
		objects []runtime.Object
		// wantErrContains is checked against the failure; empty means success.
		wantErrContains string
	}{
		{
			name:    "When every matching pod is ready it should return",
			objects: []runtime.Object{collectorPod("collector-0", true), collectorPod("collector-1", true)},
		},
		{
			name: "When no pod matches the selector it should time out rather than report ready",
			// An empty namespace is "not up yet", never "up": a readiness wait
			// that passed on zero pods would green-light a release that never
			// scheduled anything.
			objects:         nil,
			wantErrContains: "did not become ready",
		},
		{
			name:            "When one matching pod is not ready it should time out",
			objects:         []runtime.Object{collectorPod("collector-0", true), collectorPod("collector-1", false)},
			wantErrContains: "did not become ready",
		},
		{
			name: "When a pod reports no Ready condition at all it should time out",
			objects: []runtime.Object{func() runtime.Object {
				pod := collectorPod("collector-0", true)
				pod.Status.Conditions = nil
				return pod
			}()},
			wantErrContains: "did not become ready",
		},
		{
			name: "When only another workload's pod is ready it should not match it",
			// Guards the selector: a ready pod in the same namespace that the
			// chart did not render must not satisfy this wait.
			objects:         []runtime.Object{otherPod("unrelated-0")},
			wantErrContains: "did not become ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &clusterRuntime{client: fake.NewSimpleClientset(tt.objects...)}

			// One evaluation, then an immediate timeout: a ready workload
			// returns on that first poll, and anything else has to report the
			// timeout rather than claim readiness. Keeping the bound below the
			// poll interval also keeps these tests instant.
			err := rt.waitForWorkloadReady(context.Background(), readyTestNamespace, readyTestSelector, time.Nanosecond)

			if tt.wantErrContains == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErrContains)
		})
	}
}

// TestWaitForWorkloadReadyReportsAPIFailures covers the difference between
// "not ready yet" and "the question could not be asked": an API failure has to
// surface immediately rather than be retried until the timeout and reported as
// a readiness problem.
func TestWaitForWorkloadReadyReportsAPIFailures(t *testing.T) {
	listFailure := errors.New("connection refused")
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, listFailure
	})
	rt := &clusterRuntime{client: client}

	err := rt.waitForWorkloadReady(context.Background(), readyTestNamespace, readyTestSelector, time.Minute)

	require.ErrorIs(t, err, listFailure)
	require.NotContains(t, err.Error(), "did not become ready",
		"an unreachable API is not a readiness verdict")
}

// TestWaitForWorkloadReadyHonoursContextCancellation covers the bound a stuck
// cluster is held to: the wait ends with the context rather than running on.
func TestWaitForWorkloadReadyHonoursContextCancellation(t *testing.T) {
	rt := &clusterRuntime{client: fake.NewSimpleClientset(collectorPod("collector-0", false))}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rt.waitForWorkloadReady(ctx, readyTestNamespace, readyTestSelector, time.Minute)

	require.ErrorIs(t, err, context.Canceled)
}

// TestWaitForWorkloadReadyRejectsIncompleteArguments covers the guards that
// keep a mistyped call from waiting on everything in a namespace.
func TestWaitForWorkloadReadyRejectsIncompleteArguments(t *testing.T) {
	rt := &clusterRuntime{client: fake.NewSimpleClientset()}

	require.ErrorContains(t,
		rt.waitForWorkloadReady(context.Background(), "", readyTestSelector, time.Minute),
		"namespace and label selector are required")
	require.ErrorContains(t,
		rt.waitForWorkloadReady(context.Background(), readyTestNamespace, "  ", time.Minute),
		"namespace and label selector are required")
	require.ErrorContains(t,
		rt.waitForWorkloadReady(context.Background(), readyTestNamespace, readyTestSelector, 0),
		"timeout must be positive")
}

// TestPodReady pins what the readiness decision reads, since a pod can carry
// several conditions and only one of them answers this question.
func TestPodReady(t *testing.T) {
	tests := []struct {
		name       string
		conditions []corev1.PodCondition
		want       bool
	}{
		{
			name:       "When the Ready condition is true it should report ready",
			conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			want:       true,
		},
		{
			name:       "When the Ready condition is false it should report not ready",
			conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			want:       false,
		},
		{
			name:       "When the Ready condition is unknown it should report not ready",
			conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionUnknown}},
			want:       false,
		},
		{
			name:       "When there are no conditions it should report not ready",
			conditions: nil,
			want:       false,
		},
		{
			name: "When only other conditions are true it should report not ready",
			// Scheduled and Initialized are true long before the container
			// serves anything, so neither may be mistaken for readiness.
			conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
			},
			want: false,
		},
		{
			name: "When Ready is true alongside other conditions it should report ready",
			conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{Conditions: tt.conditions}}
			require.Equal(t, tt.want, podReady(pod))
		})
	}
}

// TestCollectorInstanceWaitReadyUsesTheChartSelector covers the closest real
// consumer: the instance's own namespace and selector have to be the ones the
// readiness wait is performed with, against a real client.
func TestCollectorInstanceWaitReadyUsesTheChartSelector(t *testing.T) {
	rt := &clusterRuntime{client: fake.NewSimpleClientset(collectorPod("collector-0", true), otherPod("unrelated-0"))}
	instance := &collectorInstance{
		runtime:     rt,
		namespace:   collectorNamespace("abc123"),
		releaseName: collectorReleaseName("abc123"),
		selector:    readyTestSelector,
		stopTimeout: time.Minute,
	}
	require.Equal(t, readyTestNamespace, instance.namespace,
		"the instance namespace must be the one the pods live in")

	require.NoError(t, instance.WaitReady(context.Background(), time.Minute))
}

// TestNewClusterRuntimeCarriesProviderClusterCoordinates confirms the runtime
// targets the same cluster as the rest of the infra providers, so a chart can
// never land somewhere else than the deployment under test.
func TestNewClusterRuntimeCarriesProviderClusterCoordinates(t *testing.T) {
	infraP := &InfraProvider{
		kubeConfig:  "/tmp/e2e-kubeconfig",
		kubeContext: "kind-flightctl-e2e",
		envType:     "kind",
	}

	rt, err := newClusterRuntime(fake.NewSimpleClientset(), infraP)
	require.NoError(t, err)

	require.Equal(t, "/tmp/e2e-kubeconfig", rt.kubeConfig)
	require.Equal(t, "kind-flightctl-e2e", rt.kubeContext)
	require.Equal(t, "kind", rt.envType)
	require.Equal(t,
		[]string{"--kubeconfig", "/tmp/e2e-kubeconfig", "--kube-context", "kind-flightctl-e2e"},
		rt.helmKubeArgs(),
		"every helm invocation must carry the provider's own cluster coordinates")
}
