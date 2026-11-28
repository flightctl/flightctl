package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/registryconfig"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type registryFixtureComposer func([]registryconfig.PullNeed) (*registryconfig.Composition, error)

func (f registryFixtureComposer) Compose(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
	return f(needs)
}

func registryRenderLogic(t *testing.T, org uuid.UUID, compose registryFixtureComposer) *DeviceRenderLogic {
	t.Helper()
	repo := repositoryservice.NewMockService(gomock.NewController(t))
	repo.EXPECT().ListRepositories(gomock.Any(), org, gomock.Any()).Return(&domain.RepositoryList{}, domain.StatusOK()).AnyTimes()
	return NewDeviceRenderLogic(logrus.New(), nil, repo, nil, nil, newTestKVStore(), &config.Config{}).WithRegistryConfigSnapshotFactory(func(id uuid.UUID, candidates []registryconfig.Candidate) (registryconfig.Composer, error) {
		require.Equal(t, org, id)
		return compose, nil
	})
}

func emptyRegistryRepositories(ctrl *gomock.Controller) repositoryservice.Service {
	repo := repositoryservice.NewMockService(ctrl)
	repo.EXPECT().ListRepositories(gomock.Any(), gomock.Any(), gomock.Any()).Return(&domain.RepositoryList{}, domain.StatusOK()).AnyTimes()
	return repo
}

func registrySpec(t *testing.T, raw string) *domain.DeviceSpec {
	t.Helper()
	var spec domain.DeviceSpec
	require.NoError(t, json.Unmarshal([]byte(raw), &spec))
	return &spec
}

func TestRegistryRenderNeeds(t *testing.T) {
	cases := []struct {
		name, raw string
		refs      []string
		consumer  registryconfig.Consumer
		user      string
	}{
		{"When a named container has volumes and deltas it should retain its identity", `{"applications":[{"name":"app","appType":"container","runAs":"alice","image":"quay.io/team/app:v1","deltaImage":"delta.example.com/team/app:v1","volumes":[{"name":"data","image":{"reference":"vol.example.com/data:v1"}}]}]}`, []string{"quay.io/team/app:v1", "delta.example.com/team/app:v1", "vol.example.com/data:v1"}, registryconfig.ConsumerContainer, "alice"},
		{"When Compose is inline it should use process identity", `{"applications":[{"name":"app","appType":"compose","inline":[{"path":"docker-compose.yaml","content":"services:\n  app:\n    image: quay.io/team/app:v1\n"}]}]}`, []string{"quay.io/team/app:v1"}, registryconfig.ConsumerContainer, ""},
		{"When Quadlet is inline it should use effective runAs", `{"applications":[{"name":"app","appType":"quadlet","runAs":"bob","inline":[{"path":"app.container","content":"[Container]\nImage=quay.io/team/app:v1\n"}]}]}`, []string{"quay.io/team/app:v1"}, registryconfig.ConsumerContainer, "bob"},
		{"When Helm has a chart it should use the chart system context", `{"applications":[{"name":"app","appType":"helm","image":"oci://charts.example.com/team/chart:1"}]}`, []string{"oci://charts.example.com/team/chart:1"}, registryconfig.ConsumerHelmChart, "root"},
		{"When a container has an image-mount volume it should include the resolved image", `{"applications":[{"name":"app","appType":"container","image":"quay.io/team/app:v1","volumes":[{"name":"data","image":{"reference":"vol.example.com/data:v1"},"mount":{"path":"/data"}}]}]}`, []string{"quay.io/team/app:v1", "vol.example.com/data:v1"}, registryconfig.ConsumerContainer, ""},
		{"When a Quadlet drop-in changes the image it should collect only the effective image", `{"applications":[{"name":"app","appType":"quadlet","inline":[{"path":"app.container","content":"[Container]\nImage=old.example.com/app:v1\n"},{"path":"app.container.d/override.conf","content":"[Container]\nImage=quay.io/team/app:v2\n"}]}]}`, []string{"quay.io/team/app:v2"}, registryconfig.ConsumerContainer, ""},
		{"When Quadlet references Pod YAML it should collect workload and init images", `{"applications":[{"name":"app","appType":"quadlet","inline":[{"path":"app.kube","content":"[Kube]\nYaml=pod.yaml\n"},{"path":"pod.yaml","content":"apiVersion: v1\nkind: Pod\nspec:\n  initContainers:\n    - name: init\n      image: init.example.com/init:v1\n  containers:\n    - name: app\n      image: quay.io/team/app:v1\n"}]}]}`, []string{"init.example.com/init:v1", "quay.io/team/app:v1"}, registryconfig.ConsumerContainer, ""},
		{"When optional inline parsing fails it should retain volume and hint needs", `{"applications":[{"name":"app","appType":"compose","inline":[{"path":"docker-compose.yaml","content":"invalid"}],"deltaImages":[{"targetImage":"quay.io/team/app:v1","targetDigest":"sha256:abc","deltaImage":"delta.example.com/app:v1"}]}]}`, []string{"quay.io/team/app:v1", "delta.example.com/app:v1"}, registryconfig.ConsumerContainer, ""},
		{"When an OS image is desired it should use the OS system context", `{"os":{"image":"os.example.com/team/os:v1"}}`, []string{"os.example.com/team/os:v1"}, registryconfig.ConsumerOS, "root"},
		{"When a package has nested delta hints it should include actual delta and target locations", `{"applications":[{"name":"app","appType":"compose","image":"quay.io/packages/app:v1","deltaImages":[{"targetImage":"images.example.com/app:v1","targetDigest":"sha256:abc","deltaImage":"delta.example.com/app:v1"}]}]}`, []string{"quay.io/packages/app:v1", "images.example.com/app:v1", "delta.example.com/app:v1"}, registryconfig.ConsumerContainer, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			org := uuid.New()
			called := false
			logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
				called = true
				refs := []string{}
				for _, n := range needs {
					refs = append(refs, n.Reference)
					require.Equal(t, tc.consumer, n.Consumer)
					require.Equal(t, tc.user, n.User)
					require.NotEmpty(t, n.Destination)
				}
				require.ElementsMatch(t, tc.refs, refs)
				return &registryconfig.Composition{}, nil
			})
			rendered, err := logic.RenderSpec(context.Background(), org, registrySpec(t, tc.raw))
			require.NoError(t, err)
			require.True(t, called)
			require.Empty(t, rendered.ConfigResources)
		})
	}
}

func registryComposition(needs []registryconfig.PullNeed) *registryconfig.Composition {
	result := &registryconfig.Composition{Endpoints: []registryconfig.EndpointConfig{{Registry: "quay.io", Scheme: api.Https}}}
	for _, need := range needs {
		result.Selections = append(result.Selections, registryconfig.Selection{Need: need, Registry: "quay.io", Scope: "team", Outcome: registryconfig.OutcomeAllowed})
	}
	return result
}

func TestRegistryRenderDeterminism(t *testing.T) {
	org := uuid.New()
	spec := registrySpec(t, `{"applications":[{"name":"a","appType":"container","image":"quay.io/team/a:v1","runAs":"alice"},{"name":"b","appType":"container","image":"quay.io/team/b:v1","runAs":"bob"}]}`)
	reverse := false
	logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
		r := registryComposition(needs)
		if reverse {
			slices.Reverse(r.Selections)
		}
		return r, nil
	})
	first, err := logic.RenderSpec(context.Background(), org, spec)
	require.NoError(t, err)
	reverse = true
	slices.Reverse(*spec.Applications)
	second, err := logic.RenderSpec(context.Background(), org, spec)
	require.NoError(t, err)
	require.Equal(t, string(first.ConfigResources), string(second.ConfigResources))
	var records []api.ConfigResource
	require.NoError(t, json.Unmarshal(first.ConfigResources, &records))
	require.Len(t, records, 1)
	r, err := records[0].AsRegistryConfigResource()
	require.NoError(t, err)
	require.Equal(t, "registry:quay.io", r.Name)
	require.Equal(t, api.RegistrySchemeHTTPS, r.Scheme)
	require.NotNil(t, r.SkipServerVerification)
	require.False(t, *r.SkipServerVerification)
	require.Len(t, r.Access, 1)
	require.Len(t, r.Access[0].Consumers, 2)
}

func TestRegistryRenderAuthoredPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		suppressed bool
	}{
		{"When root auth is authored it should suppress root credential selection", "/root/.config/containers/auth.json", true},
		{"When another home is authored it should retain selection", "/home/alice/.config/containers/auth.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := uuid.New()
			spec := registrySpec(t, `{"applications":[{"name":"app","appType":"container","runAs":"root","image":"quay.io/team/app:v1"}]}`)
			var cfg domain.ConfigProviderSpec
			require.NoError(t, cfg.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{Inline: []api.FileSpec{{Path: tc.path, Content: "{}"}, {Path: "/etc/containers/registries.conf", Content: ""}}}))
			spec.Config = &[]domain.ConfigProviderSpec{cfg}
			logic := registryRenderLogic(t, org, func(needs []registryconfig.PullNeed) (*registryconfig.Composition, error) {
				require.Len(t, needs, 1)
				require.Equal(t, tc.suppressed, needs[0].SuppressAuthentication)
				var supplier domain.Repository
				require.NoError(t, json.Unmarshal([]byte(`{"spec":{"type":"oci","registry":"quay.io","ociAuth":{"authType":"docker","username":"user","password":"secret"}}}`), &supplier))
				result := registryComposition(needs)
				result.Selections[0].Supplier = &supplier
				result.Selections[0].Outcome = registryconfig.OutcomeSelected
				return result, nil
			})
			rendered, err := logic.RenderSpec(context.Background(), org, spec)
			require.NoError(t, err)
			if tc.suppressed {
				require.Empty(t, rendered.ConfigResources)
			} else {
				require.NotEmpty(t, rendered.ConfigResources)
			}
		})
	}
}

func TestRegistryRenderComposerError(t *testing.T) {
	org := uuid.New()
	want := errors.New("credential ambiguity at quay.io: repositories a, b")
	logic := registryRenderLogic(t, org, func([]registryconfig.PullNeed) (*registryconfig.Composition, error) { return nil, want })
	_, err := logic.RenderSpec(context.Background(), org, &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "quay.io/os:v1"}})
	require.ErrorIs(t, err, want)
}

func TestRegistryRenderNoNeeds(t *testing.T) {
	logic := NewDeviceRenderLogic(logrus.New(), nil, nil, nil, nil, newTestKVStore(), &config.Config{})
	result, err := logic.RenderSpec(context.Background(), uuid.New(), &domain.DeviceSpec{})
	require.NoError(t, err)
	require.Empty(t, result.ConfigResources)
}

func TestRegistryRenderPaging(t *testing.T) {
	org := uuid.New()
	repo := repositoryservice.NewMockService(gomock.NewController(t))
	next := "next"
	gomock.InOrder(
		repo.EXPECT().ListRepositories(gomock.Any(), org, domain.ListRepositoriesParams{}).Return(&domain.RepositoryList{Items: []domain.Repository{{Metadata: domain.ObjectMeta{Name: lo.ToPtr("a")}}}, Metadata: domain.ListMeta{Continue: &next}}, domain.StatusOK()),
		repo.EXPECT().ListRepositories(gomock.Any(), org, domain.ListRepositoriesParams{Continue: &next}).Return(&domain.RepositoryList{Items: []domain.Repository{{Metadata: domain.ObjectMeta{Name: lo.ToPtr("b")}}}}, domain.StatusOK()),
	)
	called := 0
	logic := NewDeviceRenderLogic(logrus.New(), nil, repo, nil, nil, newTestKVStore(), &config.Config{}).WithRegistryConfigSnapshotFactory(func(id uuid.UUID, candidates []registryconfig.Candidate) (registryconfig.Composer, error) {
		called++
		require.Equal(t, org, id)
		require.Len(t, candidates, 2)
		for _, c := range candidates {
			require.Equal(t, org, c.OrganizationID)
		}
		return registryFixtureComposer(func([]registryconfig.PullNeed) (*registryconfig.Composition, error) {
			return &registryconfig.Composition{}, nil
		}), nil
	})
	_, err := logic.RenderSpec(context.Background(), org, &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "quay.io/os:v1"}})
	require.NoError(t, err)
	require.Equal(t, 1, called)
}
