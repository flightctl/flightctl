package util

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/wait"
)

func CreateTestOrganization(ctx context.Context, storeInst store.Store, orgId uuid.UUID) error {
	externalID := fmt.Sprintf("external-id-%s", orgId.String())
	org := &model.Organization{
		ID:          orgId,
		ExternalID:  externalID,
		DisplayName: "Test Organization",
	}
	_, err := storeInst.Organization().Create(ctx, org)
	if err != nil {
		return err
	}
	return nil
}

func ReturnTestDevice(orgId uuid.UUID, name string, owner *string, tv *string, labels *map[string]string) api.Device {
	deviceStatus := api.NewDeviceStatus()
	deviceStatus.Os.Image = "quay.io/flightctl/test-osimage:latest"

	gitConfig := &api.GitConfigProviderSpec{
		Name: "param-git-config",
	}
	gitConfig.GitRef.Path = "path-{{ device.metadata.labels[key] }}"
	gitConfig.GitRef.Repository = "repo"
	gitConfig.GitRef.TargetRevision = "rev"
	gitItem := api.ConfigProviderSpec{}
	_ = gitItem.FromGitConfigProviderSpec(*gitConfig)

	inlineConfig := &api.InlineConfigProviderSpec{
		Name: "param-inline-config",
	}
	enc := api.EncodingBase64
	inlineConfig.Inline = []api.FileSpec{
		// Unencoded: My version is {{ device.metadata.labels[version] }}
		{Path: "/etc/withparams", ContentEncoding: &enc, Content: "TXkgdmVyc2lvbiBpcyB7eyBkZXZpY2UubWV0YWRhdGEubGFiZWxzW3ZlcnNpb25dIH19"},
	}
	inlineItem := api.ConfigProviderSpec{}
	_ = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)

	httpConfig := &api.HttpConfigProviderSpec{
		Name: "param-http-config",
	}
	httpConfig.HttpRef.Repository = "http-repo"
	httpConfig.HttpRef.FilePath = "/http-path-{{ device.metadata.labels[key] }}"
	httpConfig.HttpRef.Suffix = lo.ToPtr("/http-suffix")
	httpItem := api.ConfigProviderSpec{}
	_ = httpItem.FromHttpConfigProviderSpec(*httpConfig)

	resource := api.Device{
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: labels,
			Owner:  owner,
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOsSpec{
				Image: "os",
			},
			Config: &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem},
		},
		Status: &deviceStatus,
	}

	if tv != nil {
		rv := *tv
		annotations := map[string]string{
			api.DeviceAnnotationTemplateVersion: rv,
		}
		resource.Metadata.Annotations = &annotations
		deviceStatus.Config.RenderedVersion = rv
	}

	return resource
}

func CreateTestDevice(ctx context.Context, deviceStore store.Device, orgId uuid.UUID, name string, owner *string, tv *string, labels *map[string]string) {
	resource := ReturnTestDevice(orgId, name, owner, tv, labels)
	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := deviceStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		log.Fatalf("creating device: %v", err)
	}
}

func CreateTestDevices(ctx context.Context, numDevices int, deviceStore store.Device, orgId uuid.UUID, owner *string, sameVals bool) {
	CreateTestDevicesWithOffset(ctx, numDevices, deviceStore, orgId, owner, sameVals, 0)
}

func CreateTestDevicesWithOffset(ctx context.Context, numDevices int, deviceStore store.Device, orgId uuid.UUID, owner *string, sameVals bool, offset int) {
	for i := 1; i <= numDevices; i++ {
		num := i + offset
		labels := map[string]string{"key": fmt.Sprintf("value-%d", num), "otherkey": "othervalue", "version": fmt.Sprintf("%d", num)}
		if sameVals {
			labels["key"] = "value"
			labels["version"] = "1"
		}

		CreateTestDevice(ctx, deviceStore, orgId, fmt.Sprintf("mydevice-%d", num), owner, nil, &labels)
	}
}

func CreateTestFleet(ctx context.Context, fleetStore store.Fleet, orgId uuid.UUID, name string, selector *map[string]string, owner *string) {
	resource := api.Fleet{
		Metadata: api.ObjectMeta{
			Name:   &name,
			Labels: selector,
			Owner:  owner,
		},
	}

	if selector != nil {
		resource.Spec.Selector = &api.LabelSelector{MatchLabels: selector}
	}
	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := fleetStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		log.Fatalf("creating fleet: %v", err)
	}
}

func CreateTestFleets(ctx context.Context, numFleets int, fleetStore store.Fleet, orgId uuid.UUID, namePrefix string, sameVals bool, owner *string) {
	for i := 1; i <= numFleets; i++ {
		selector := map[string]string{"key": fmt.Sprintf("value-%d", i)}
		if sameVals {
			selector["key"] = "value"
		}
		CreateTestFleet(ctx, fleetStore, orgId, fmt.Sprintf("%s-%d", namePrefix, i), &selector, owner)
	}
}

func CreateTestTemplateVersion(ctx context.Context, tvStore store.TemplateVersion, orgId uuid.UUID, fleet, name string, status *api.TemplateVersionStatus) error {
	owner := util.SetResourceOwner(api.FleetKind, fleet)
	resource := api.TemplateVersion{
		Metadata: api.ObjectMeta{
			Name:  &name,
			Owner: owner,
		},
		Spec: api.TemplateVersionSpec{
			Fleet: fleet,
		},
		Status: &api.TemplateVersionStatus{},
	}
	if status != nil {
		resource.Status = status
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := tvStore.Create(ctx, orgId, &resource, callback)

	return err
}

func CreateTestTemplateVersions(ctx context.Context, numTemplateVersions int, tvStore store.TemplateVersion, orgId uuid.UUID, fleet string) error {
	for i := 1; i <= numTemplateVersions; i++ {
		status := api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "myimage"}}
		err := CreateTestTemplateVersion(ctx, tvStore, orgId, fleet, fmt.Sprintf("1.0.%d", i), &status)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateRepositories(ctx context.Context, numRepositories int, storeInst store.Store, orgId uuid.UUID) error {
	for i := 1; i <= numRepositories; i++ {
		spec := api.RepositorySpec{}
		err := spec.FromGitRepoSpec(api.GitRepoSpec{
			Url:  "myrepo",
			Type: api.GitRepoSpecTypeGit,
		})
		if err != nil {
			return err
		}
		resource := api.Repository{
			Metadata: api.ObjectMeta{
				Name:   lo.ToPtr(fmt.Sprintf("myrepository-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: spec,
		}

		callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		_, err = storeInst.Repository().Create(ctx, orgId, &resource, callback)
		if err != nil {
			return err
		}
	}
	return nil
}

func CreateTestEnrolmentRequests(numEnrollmentRequests int, ctx context.Context, store store.Store, orgId uuid.UUID) {
	for i := 1; i <= numEnrollmentRequests; i++ {
		resource := api.EnrollmentRequest{
			Metadata: api.ObjectMeta{
				Name:   lo.ToPtr(fmt.Sprintf("myenrollmentrequest-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.EnrollmentRequestSpec{
				Csr: "csr string",
			},
			Status: &api.EnrollmentRequestStatus{
				Certificate: lo.ToPtr("cert"),
			},
		}

		_, err := store.EnrollmentRequest().Create(ctx, orgId, &resource, nil)
		if err != nil {
			log.Fatalf("creating enrollmentrequest: %v", err)
		}
		_, err = store.EnrollmentRequest().UpdateStatus(ctx, orgId, &resource, nil)
		if err != nil {
			log.Fatalf("updating enrollmentrequest status: %v", err)
		}
	}
}

func CreateTestResourceSyncs(ctx context.Context, numResourceSyncs int, storeInst store.Store, orgId uuid.UUID) {
	for i := 1; i <= numResourceSyncs; i++ {
		resource := api.ResourceSync{
			Metadata: api.ObjectMeta{
				Name:   lo.ToPtr(fmt.Sprintf("myresourcesync-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.ResourceSyncSpec{
				Repository: "myrepo",
				Path:       "my/path",
			},
		}

		_, err := storeInst.ResourceSync().Create(ctx, orgId, &resource, nil)
		if err != nil {
			log.Fatalf("creating resourcesync: %v", err)
		}
	}
}

func NewBackoff() wait.Backoff {
	return wait.Backoff{
		Steps: 1,
	}
}

func NewPollConfig() poll.Config {
	return poll.Config{
		BaseDelay:    1 * time.Millisecond,
		Factor:       1.0,
		MaxDelay:     1 * time.Millisecond,
		JitterFactor: 0.1,
	}
}

func NewComposeSpec(images ...string) string {
	if len(images) == 0 {
		images = []string{"quay.io/flightctl-tests/alpine:v1"}
	}

	var sb strings.Builder
	sb.WriteString(`version: "3"
services:
`)

	for i, img := range images {
		fmt.Fprintf(&sb, "  service%d:\n", i+1)
		fmt.Fprintf(&sb, "    image: %s\n", img)
		sb.WriteString(`    command: ["sleep", "infinity"]
`)
	}

	return sb.String()
}

// ReturnTestAuthProvider creates a test auth provider with the given parameters
func ReturnTestAuthProvider(orgId uuid.UUID, name string, issuer string, labels *map[string]string) api.AuthProvider {
	if issuer == "" {
		issuer = "https://accounts.google.com"
	}

	// Create organization assignment
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}
	if err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment); err != nil {
		panic(fmt.Sprintf("failed to create organization assignment: %v", err))
	}

	// Create role assignment
	roleAssignment := api.AuthRoleAssignment{}
	staticRoleAssignment := api.AuthStaticRoleAssignment{
		Type:  api.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{api.ExternalRoleViewer},
	}
	if err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment); err != nil {
		panic(fmt.Sprintf("failed to create role assignment: %v", err))
	}

	// Create OIDC provider spec
	oidcSpec := api.OIDCProviderSpec{
		ProviderType:           api.Oidc,
		Issuer:                 issuer,
		ClientId:               fmt.Sprintf("test-client-id-%s", name),
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
		Enabled:                lo.ToPtr(true),
		UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
		RoleAssignment:         roleAssignment,
		OrganizationAssignment: assignment,
	}

	// Create AuthProvider with OIDC spec
	authProvider := api.AuthProvider{
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: labels,
		},
	}
	if err := authProvider.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
		panic(fmt.Sprintf("failed to create auth provider spec: %v", err))
	}

	return authProvider
}

// CreateTestAuthProvider creates a test auth provider in the store
func CreateTestAuthProvider(ctx context.Context, authStore store.AuthProvider, orgId uuid.UUID, name string, issuer string, labels *map[string]string) {
	resource := ReturnTestAuthProvider(orgId, name, issuer, labels)
	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := authStore.Create(ctx, orgId, &resource, callback)
	if err != nil {
		log.Fatalf("creating auth provider: %v", err)
	}
}

// CreateTestAuthProviders creates multiple test auth providers
func CreateTestAuthProviders(ctx context.Context, numProviders int, authStore store.AuthProvider, orgId uuid.UUID, sameVals bool) {
	CreateTestAuthProvidersWithOffset(ctx, numProviders, authStore, orgId, sameVals, 0)
}

// CreateTestAuthProvidersWithOffset creates multiple test OIDC providers with an offset
func CreateTestAuthProvidersWithOffset(ctx context.Context, numProviders int, authStore store.AuthProvider, orgId uuid.UUID, sameVals bool, offset int) {
	issuers := []string{
		"https://accounts.google.com",
		"https://login.microsoftonline.com",
		"https://auth0.com",
		"https://keycloak.example.com",
		"https://okta.com",
	}

	for i := 1; i <= numProviders; i++ {
		num := i + offset
		labels := map[string]string{
			"key":      fmt.Sprintf("value-%d", num),
			"otherkey": "othervalue",
			"version":  fmt.Sprintf("%d", num),
			"type":     "oidc",
		}
		if sameVals {
			labels["key"] = "value"
			labels["version"] = "1"
		}

		issuer := issuers[(num-1)%len(issuers)]
		CreateTestAuthProvider(ctx, authStore, orgId, fmt.Sprintf("myoidcprovider-%d", num), issuer, &labels)
	}
}

// CreateTestAuthProviderWithStaticOrg creates a test OIDC provider with static organization assignment
func CreateTestAuthProviderWithStaticOrg(ctx context.Context, authStore store.AuthProvider, orgId uuid.UUID, name string, orgName string) {
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: orgName,
	}
	if err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment); err != nil {
		log.Fatalf("failed to create organization assignment: %v", err)
	}

	// Create OIDC provider spec
	oidcSpec := api.OIDCProviderSpec{
		ProviderType:           api.Oidc,
		Issuer:                 "https://accounts.google.com",
		ClientId:               fmt.Sprintf("test-client-id-%s", name),
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
		Enabled:                lo.ToPtr(true),
		UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
		OrganizationAssignment: assignment,
	}

	provider := api.AuthProvider{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
		},
	}
	if err := provider.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
		log.Fatalf("failed to create auth provider spec: %v", err)
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := authStore.Create(ctx, orgId, &provider, callback)
	if err != nil {
		log.Fatalf("creating OIDC provider with static org: %v", err)
	}
}

// CreateTestAuthProviderWithDynamicOrg creates a test OIDC provider with dynamic organization assignment
func CreateTestAuthProviderWithDynamicOrg(ctx context.Context, authStore store.AuthProvider, orgId uuid.UUID, name string) {
	assignment := api.AuthOrganizationAssignment{}
	dynamicAssignment := api.AuthDynamicOrganizationAssignment{
		Type:      api.AuthDynamicOrganizationAssignmentTypeDynamic,
		ClaimPath: []string{"organization"},
	}
	if err := assignment.FromAuthDynamicOrganizationAssignment(dynamicAssignment); err != nil {
		log.Fatalf("failed to create organization assignment: %v", err)
	}

	// Create role assignment
	roleAssignment := api.AuthRoleAssignment{}
	staticRoleAssignment := api.AuthStaticRoleAssignment{
		Type:  api.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{api.ExternalRoleViewer},
	}
	if err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment); err != nil {
		log.Fatalf("failed to create role assignment: %v", err)
	}

	// Create OIDC provider spec
	oidcSpec := api.OIDCProviderSpec{
		ProviderType:           api.Oidc,
		Issuer:                 "https://accounts.google.com",
		ClientId:               fmt.Sprintf("test-client-id-%s", name),
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
		Enabled:                lo.ToPtr(true),
		UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
		RoleAssignment:         roleAssignment,
		OrganizationAssignment: assignment,
	}

	provider := api.AuthProvider{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
		},
	}
	if err := provider.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
		log.Fatalf("failed to create auth provider spec: %v", err)
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := authStore.Create(ctx, orgId, &provider, callback)
	if err != nil {
		log.Fatalf("creating OIDC provider with dynamic org: %v", err)
	}
}

// CreateTestAuthProviderWithPerUserOrg creates a test OIDC provider with per-user organization assignment
func CreateTestAuthProviderWithPerUserOrg(ctx context.Context, authStore store.AuthProvider, orgId uuid.UUID, name string, prefix *string, suffix *string) {
	assignment := api.AuthOrganizationAssignment{}
	perUserAssignment := api.AuthPerUserOrganizationAssignment{
		Type:                   api.PerUser,
		OrganizationNamePrefix: prefix,
		OrganizationNameSuffix: suffix,
	}
	if err := assignment.FromAuthPerUserOrganizationAssignment(perUserAssignment); err != nil {
		log.Fatalf("failed to create organization assignment: %v", err)
	}

	// Create role assignment
	roleAssignment := api.AuthRoleAssignment{}
	staticRoleAssignment := api.AuthStaticRoleAssignment{
		Type:  api.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{api.ExternalRoleViewer},
	}
	if err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment); err != nil {
		log.Fatalf("failed to create role assignment: %v", err)
	}

	// Create OIDC provider spec
	oidcSpec := api.OIDCProviderSpec{
		ProviderType:           api.Oidc,
		Issuer:                 "https://accounts.google.com",
		ClientId:               fmt.Sprintf("test-client-id-%s", name),
		ClientSecret:           lo.ToPtr("test-client-secret"),
		Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
		Enabled:                lo.ToPtr(true),
		UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
		RoleAssignment:         roleAssignment,
		OrganizationAssignment: assignment,
	}

	provider := api.AuthProvider{
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
		},
	}
	if err := provider.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
		log.Fatalf("failed to create auth provider spec: %v", err)
	}

	callback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
	_, err := authStore.Create(ctx, orgId, &provider, callback)
	if err != nil {
		log.Fatalf("creating OIDC provider with per-user org: %v", err)
	}
}

// CreateTestOrganizationAssignment creates a test organization assignment
func CreateTestOrganizationAssignment() api.AuthOrganizationAssignment {
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "default-org",
	}
	if err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment); err != nil {
		panic(fmt.Sprintf("failed to create organization assignment: %v", err))
	}
	return assignment
}

func BuildInlineConfigSpec(name, filePath, content, user string) (api.ConfigProviderSpec, error) {
	if name == "" || filePath == "" {
		return api.ConfigProviderSpec{}, fmt.Errorf("name and path must not be empty")
	}
	fileSpec := api.FileSpec{
		Path:    filePath,
		Content: content,
	}
	if user != "" {
		fileSpec.User = api.Username(user)
	}
	var provider api.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
		Name:   name,
		Inline: []api.FileSpec{fileSpec},
	})
	return provider, err
}
