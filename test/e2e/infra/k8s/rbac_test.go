package k8s

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCreateOrganizationUsesHelmReleaseLabel(t *testing.T) {
	t.Run("When the API namespace differs from the Helm release it should label the new organization with the release", func(t *testing.T) {
		client := fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "flightctl-api", Namespace: "flightctl-external", Labels: map[string]string{"app.kubernetes.io/instance": "flightctl"},
		}})
		provider, err := NewRBACProvider(client, "flightctl-external")
		require.NoError(t, err)
		require.NoError(t, provider.CreateOrganization(context.Background(), "test-organization"))
		organization, err := client.CoreV1().Namespaces().Get(context.Background(), "test-organization", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "flightctl", organization.Labels[infra.OrgLabelKey])
	})
	for _, testCase := range []struct {
		name       string
		deployment *appsv1.Deployment
		wantError  string
	}{
		{name: "When the API deployment is missing it should not create an organization", wantError: "get API deployment"},
		{
			name: "When the API deployment has no release label it should not create an organization",
			deployment: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
				Name: "flightctl-api", Namespace: "flightctl-external", Labels: map[string]string{"app.kubernetes.io/instance": ""},
			}},
			wantError: "has no Helm release label",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			if testCase.deployment != nil {
				_, err := client.AppsV1().Deployments("flightctl-external").Create(context.Background(), testCase.deployment, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			provider, err := NewRBACProvider(client, "flightctl-external")
			require.NoError(t, err)
			require.ErrorContains(t, provider.CreateOrganization(context.Background(), "test-organization"), testCase.wantError)
			_, err = client.CoreV1().Namespaces().Get(context.Background(), "test-organization", metav1.GetOptions{})
			require.True(t, apierrors.IsNotFound(err), "organization namespace should not have been created: %v", err)
		})
	}
}
