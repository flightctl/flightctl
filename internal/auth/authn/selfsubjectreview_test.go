package authn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

func TestSelfSubjectReviewUsesCallerToken(t *testing.T) {
	tests := []struct {
		name     string
		validate func(*testing.T, k8sclient.K8SClient) func(context.Context, string) error
	}{
		{
			name: "When Kubernetes validates a token it should authenticate the review as the caller",
			validate: func(t *testing.T, client k8sclient.K8SClient) func(context.Context, string) error {
				return newTestK8sAuthN(t, client).ValidateToken
			},
		},
		{
			name: "When OpenShift validates a token it should authenticate the review as the caller",
			validate: func(t *testing.T, client k8sclient.K8SClient) func(context.Context, string) error {
				return newTestOpenShiftAuth(t, client).ValidateToken
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const callerToken = "caller-token"
			authorization := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/apis/authentication.k8s.io/v1/selfsubjectreviews", r.URL.Path)
				authorization <- r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(selfSubjectReviewJSON("caller", "caller-uid", nil))
			}))
			t.Cleanup(server.Close)
			restClient, err := rest.RESTClientFor(&rest.Config{
				Host:        server.URL,
				BearerToken: "backend-service-account-token",
				ContentConfig: rest.ContentConfig{
					GroupVersion:         &schema.GroupVersion{Version: "v1"},
					NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
				},
			})
			require.NoError(t, err)

			mock := k8sclient.NewMockK8SClient(gomock.NewController(t))
			mock.EXPECT().PostCRD(gomock.Any(), "authentication.k8s.io/v1/selfsubjectreviews", gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, gvk string, body []byte, opts ...k8sclient.Option) ([]byte, error) {
					// Exercise the provider's request options through the HTTP transport, including
					// the backend credential that would be used without the caller's override.
					req := restClient.Post().AbsPath("/apis/" + gvk).Body(body)
					for _, opt := range opts {
						opt(req)
					}
					return req.DoRaw(ctx)
				})

			require.NoError(t, tt.validate(t, mock)(t.Context(), callerToken))
			require.Equal(t, "Bearer "+callerToken, <-authorization)
		})
	}
}
