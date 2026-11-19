package tasks

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/registryconfig"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func registryTestCA(t *testing.T) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestRegistryRenderAnonymousTrust(t *testing.T) {
	org := uuid.New()
	ca := registryTestCA(t)
	logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
		r := registryComposition(needs)
		r.Endpoints[0].CABundle = ca
		return r, nil
	})
	rendered, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"os":{"image":"quay.io/team/os:v1"}}`))
	require.NoError(t, err)
	var resources []api.ConfigResource
	require.NoError(t, json.Unmarshal(rendered.ConfigResources, &resources))
	require.Len(t, resources, 1)
	r, err := resources[0].AsRegistryConfigResource()
	require.NoError(t, err)
	require.Nil(t, r.Access[0].Authentication)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(ca)), lo.FromPtr(r.CaCrt))
}

func TestRegistryRenderCredentialIsolation(t *testing.T) {
	org := uuid.New()
	var supplierA, supplierB domain.Repository
	require.NoError(t, json.Unmarshal([]byte(`{"spec":{"type":"oci","registry":"quay.io","ociAuth":{"authType":"docker","username":"a","password":"secret-a"}}}`), &supplierA))
	require.NoError(t, json.Unmarshal([]byte(`{"spec":{"type":"oci","registry":"quay.io","ociAuth":{"authType":"docker","username":"b","password":"secret-b"}}}`), &supplierB))
	logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
		require.Len(t, needs, 2)
		return &registryconfig.Composition{Endpoints: []registryconfig.EndpointConfig{{Registry: "quay.io", Scheme: api.Http, SkipServerVerification: true}}, Selections: []registryconfig.Selection{
			{Need: needs[0], Registry: "quay.io", Scope: "team", Outcome: registryconfig.OutcomeSelected, Supplier: &supplierA},
			{Need: needs[1], Registry: "quay.io", Scope: "team", Outcome: registryconfig.OutcomeSelected, Supplier: &supplierB},
		}}, nil
	})
	rendered, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"applications":[{"name":"a","appType":"container","runAs":"alice","image":"quay.io/team/a:v1"},{"name":"b","appType":"container","runAs":"bob","image":"quay.io/team/b:v1"}]}`))
	require.NoError(t, err)
	var resources []api.ConfigResource
	require.NoError(t, json.Unmarshal(rendered.ConfigResources, &resources))
	require.Len(t, resources, 1)
	r, err := resources[0].AsRegistryConfigResource()
	require.NoError(t, err)
	require.True(t, lo.FromPtr(r.SkipServerVerification))
	require.Equal(t, api.RegistrySchemeHTTP, r.Scheme)
	require.Len(t, r.Access, 2)
	for i, user := range []string{"alice", "bob"} {
		require.Len(t, r.Access[i].Consumers, 1)
		require.Equal(t, user, lo.FromPtr(r.Access[i].Consumers[0].User).String())
		auth, err := r.Access[i].Authentication.AsDockerAuth()
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}[i], auth.Username)
	}
}

func TestRegistryRenderInvalidComposition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture *registryconfig.Composition
	}{
		{"When a selection has no endpoint it should fail rendering", &registryconfig.Composition{Selections: []registryconfig.Selection{{Registry: "quay.io", Outcome: registryconfig.OutcomeAllowed}}}},
		{"When a selected endpoint has invalid transport it should fail rendering", &registryconfig.Composition{Selections: []registryconfig.Selection{{Registry: "quay.io", Scope: "team", Outcome: registryconfig.OutcomeAllowed, Need: registryconfig.PullNeed{Consumer: registryconfig.ConsumerOS}}}, Endpoints: []registryconfig.EndpointConfig{{Registry: "quay.io", Scheme: "ftp"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := uuid.New()
			logic := registryRenderLogic(t, org, func([]registryconfig.PullNeed) (*registryconfig.Composition, error) { return tc.fixture, nil })
			_, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"os":{"image":"quay.io/team/os:v1"}}`))
			require.Error(t, err)
		})
	}
}

func TestRegistryRenderBlockedAndUnmatched(t *testing.T) {
	for _, outcome := range []registryconfig.Outcome{registryconfig.OutcomeBlocked, registryconfig.OutcomeUnmatched} {
		t.Run("When a need is "+string(outcome)+" it should render without a credential condition", func(t *testing.T) {
			org := uuid.New()
			logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
				return &registryconfig.Composition{Selections: []registryconfig.Selection{{Need: needs[0], Outcome: outcome}}}, nil
			})
			r, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"os":{"image":"quay.io/team/os:v1"}}`))
			require.NoError(t, err)
			require.Empty(t, r.ConfigResources)
		})
	}
}

func TestRegistryRenderRepositoryPageFailure(t *testing.T) {
	org := uuid.New()
	repos := repositoryservice.NewMockService(gomock.NewController(t))
	next := "next"
	gomock.InOrder(
		repos.EXPECT().ListRepositories(gomock.Any(), org, domain.ListRepositoriesParams{}).Return(&domain.RepositoryList{Metadata: domain.ListMeta{Continue: &next}}, domain.StatusOK()),
		repos.EXPECT().ListRepositories(gomock.Any(), org, domain.ListRepositoriesParams{Continue: &next}).Return(nil, domain.StatusInternalServerError("unavailable")),
	)
	logic := NewDeviceRenderLogic(logrus.New(), nil, repos, nil, nil, newTestKVStore(), &config.Config{}).WithRegistryConfigSnapshotFactory(func(uuid.UUID, []registryconfig.Candidate) (registryconfig.Composer, error) {
		t.Fatal("partial snapshot must not be composed")
		return nil, nil
	})
	_, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"os":{"image":"quay.io/team/os:v1"}}`))
	require.ErrorContains(t, err, "unavailable")
}

func TestRegistryRenderWriterExclusion(t *testing.T) {
	org := uuid.New()
	cfg := &config.Config{DeltaGeneration: &deltaconfig.DeltaGenerationConfig{DefaultRepository: &deltaconfig.DefaultRepositoryConfig{Registry: "writer.example.com", Username: "writer", Password: "writer-secret"}}}
	logic := NewDeviceRenderLogic(logrus.New(), nil, emptyRegistryRepositories(gomock.NewController(t)), nil, nil, newTestKVStore(), cfg).WithRegistryConfigSnapshotFactory(func(id uuid.UUID, candidates []registryconfig.Candidate) (registryconfig.Composer, error) {
		require.Empty(t, candidates)
		return registryFixtureComposer(func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
			require.Contains(t, needs, registryconfig.PullNeed{Reference: "writer.example.com/delta:v1", Consumer: registryconfig.ConsumerContainer, Destination: "containers:"})
			return &registryconfig.Composition{}, nil
		}), nil
	})
	r, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"applications":[{"name":"app","appType":"container","image":"quay.io/team/app:v1","deltaImage":"writer.example.com/delta:v1"}]}`))
	require.NoError(t, err)
	require.Empty(t, r.ConfigResources)
}

func TestRegistryRenderRuntimePolicy(t *testing.T) {
	org := uuid.New()
	logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
		var selections []registryconfig.Selection
		for _, need := range needs {
			if need.Consumer == registryconfig.ConsumerHelmImage {
				selections = append(selections, registryconfig.Selection{Need: need, Registry: "quay.io", Scope: "team", Outcome: registryconfig.OutcomeAllowed})
			}
		}
		return &registryconfig.Composition{Endpoints: []registryconfig.EndpointConfig{{Registry: "quay.io", Scheme: api.Https}}, Selections: selections}, nil
	})
	spec := registrySpec(t, `{"applications":[{"name":"chart","appType":"helm","image":"oci://charts.example.com/team/chart:1","deltaImages":[{"targetImage":"quay.io/team/app:v1","targetDigest":"sha256:abc","deltaImage":"quay.io/team/delta:v1"}]}],"config":[{"name":"policy","inline":[{"path":"/root/.config/containers/auth.json","content":"{}"},{"path":"/etc/containers/registries.conf","content":""}]}]}`)
	result, err := logic.RenderSpec(context.Background(), org, spec)
	require.NoError(t, err)
	require.NotEmpty(t, result.ConfigResources, "the server cannot establish that the configured CRI reads containers policy")
}

func TestRegistryRenderAuthoredContextIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		suppressed registryconfig.Consumer
	}{
		{"When OS auth is authored it should retain workload and Helm auth", "/etc/ostree/auth.json", registryconfig.ConsumerOS},
		{"When Helm auth is authored it should retain workload and OS auth", "/root/.config/helm/registry/config.json", registryconfig.ConsumerHelmChart},
		{"When root containers auth is authored it should retain named workload and OS auth", "/root/.config/containers/auth.json", registryconfig.ConsumerHelmChart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := uuid.New()
			ca := registryTestCA(t)
			logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
				require.Len(t, needs, 3)
				result := registryComposition(needs)
				result.Endpoints[0].CABundle = ca
				for i, need := range needs {
					require.Equal(t, need.Consumer == tc.suppressed, need.SuppressAuthentication)
					var auth api.OciAuth
					require.NoError(t, auth.FromDockerAuth(api.DockerAuth{AuthType: "docker", Username: string(need.Consumer), Password: "fixture-secret"}))
					var supplier domain.Repository
					require.NoError(t, supplier.Spec.FromOciRepoSpec(api.OciRepoSpec{Type: api.OciRepoSpecTypeOci, Registry: "quay.io", OciAuth: &auth}))
					result.Selections[i].Supplier = &supplier
					result.Selections[i].Outcome = registryconfig.OutcomeSelected
				}
				return result, nil
			})
			spec := registrySpec(t, `{"os":{"image":"quay.io/team/os:v1"},"applications":[{"name":"workload","appType":"container","runAs":"alice","image":"quay.io/team/app:v1"},{"name":"chart","appType":"helm","image":"oci://quay.io/team/chart:1"}]}`)
			var provider domain.ConfigProviderSpec
			require.NoError(t, provider.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{Inline: []api.FileSpec{{Path: tc.path, Content: "{}"}, {Path: "/etc/containers/registries.conf", Content: ""}}}))
			spec.Config = &[]domain.ConfigProviderSpec{provider}
			rendered, err := logic.RenderSpec(context.Background(), org, spec)
			require.NoError(t, err)
			var resources []api.ConfigResource
			require.NoError(t, json.Unmarshal(rendered.ConfigResources, &resources))
			require.Len(t, resources, 1)
			resource, err := resources[0].AsRegistryConfigResource()
			require.NoError(t, err)
			require.Equal(t, base64.StdEncoding.EncodeToString([]byte(ca)), lo.FromPtr(resource.CaCrt))
			require.Len(t, resource.Access, 3)
			for _, access := range resource.Access {
				require.Len(t, access.Consumers, 1)
				consumer := access.Consumers[0]
				if registryconfig.Consumer(consumer.Type) == tc.suppressed {
					require.Nil(t, access.Authentication)
				} else {
					require.NotNil(t, access.Authentication)
					auth, err := access.Authentication.AsDockerAuth()
					require.NoError(t, err)
					require.Equal(t, string(consumer.Type), auth.Username)
				}
				if consumer.Type == api.Container {
					require.Equal(t, api.Username("alice"), lo.FromPtr(consumer.User))
				} else {
					require.Equal(t, api.RootUsername, lo.FromPtr(consumer.User))
				}
			}
		})
	}
}

func TestRegistryRenderSpecForDevice(t *testing.T) {
	org := uuid.New()
	called := false
	logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
		called = true
		require.Equal(t, []registryconfig.PullNeed{{Reference: "quay.io/virt/launcher:v1", Consumer: registryconfig.ConsumerContainer, User: "vmuser", Destination: "containers:vmuser"}}, needs)
		return registryComposition(needs), nil
	}).WithVmConverter(stubbedConverter(map[string]string{"launcher.container": "[Container]\nImage=quay.io/virt/launcher:v1\n"}))
	spec := registrySpec(t, `{"applications":[{"name":"vm","appType":"vm","runAs":"vmuser","inline":[{"path":"vm.yaml","content":"kind: VirtualMachine"}]}]}`)
	rendered, err := logic.RenderSpecForDevice(context.Background(), org, &domain.Device{Status: lo.ToPtr(api.NewDeviceStatus())}, spec)
	require.NoError(t, err)
	require.True(t, called)
	require.NotEmpty(t, rendered.ConfigResources)
}

func TestRegistryRenderSnapshotFailure(t *testing.T) {
	org := uuid.New()
	want := errors.New("invalid registry snapshot")
	logic := registryRenderLogic(t, org, nil).WithRegistryConfigSnapshotFactory(func(uuid.UUID, []registryconfig.Candidate) (registryconfig.Composer, error) { return nil, want })
	_, err := logic.RenderSpec(context.Background(), org, registrySpec(t, `{"os":{"image":"quay.io/team/os:v1"}}`))
	require.ErrorIs(t, err, want)
}
