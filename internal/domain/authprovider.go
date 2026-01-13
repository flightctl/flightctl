package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Resource Types ==========

type AuthProvider = v1beta1.AuthProvider
type AuthProviderList = v1beta1.AuthProviderList
type AuthProviderSpec = v1beta1.AuthProviderSpec
type AuthConfig = v1beta1.AuthConfig

// ========== Auth Provider Spec Types ==========

type OIDCProviderSpec = v1beta1.OIDCProviderSpec
type OIDCProviderSpecProviderType = v1beta1.OIDCProviderSpecProviderType
type OAuth2ProviderSpec = v1beta1.OAuth2ProviderSpec
type OAuth2ProviderSpecProviderType = v1beta1.OAuth2ProviderSpecProviderType
type OpenShiftProviderSpec = v1beta1.OpenShiftProviderSpec
type OpenShiftProviderSpecProviderType = v1beta1.OpenShiftProviderSpecProviderType
type AapProviderSpec = v1beta1.AapProviderSpec
type AapProviderSpecProviderType = v1beta1.AapProviderSpecProviderType
type K8sProviderSpec = v1beta1.K8sProviderSpec
type K8sProviderSpecProviderType = v1beta1.K8sProviderSpecProviderType

const (
	ProviderTypeOidc      = v1beta1.Oidc
	ProviderTypeOauth2    = v1beta1.Oauth2
	ProviderTypeOpenshift = v1beta1.Openshift
	ProviderTypeAap       = v1beta1.Aap
	ProviderTypeK8s       = v1beta1.K8s

	// Direct aliases for compatibility
	Oidc      = v1beta1.Oidc
	Oauth2    = v1beta1.Oauth2
	Openshift = v1beta1.Openshift
	Aap       = v1beta1.Aap
	K8s       = v1beta1.K8s
)

// ========== Role Assignment Types ==========

type AuthRoleAssignment = v1beta1.AuthRoleAssignment
type AuthStaticRoleAssignment = v1beta1.AuthStaticRoleAssignment
type AuthStaticRoleAssignmentType = v1beta1.AuthStaticRoleAssignmentType
type AuthDynamicRoleAssignment = v1beta1.AuthDynamicRoleAssignment
type AuthDynamicRoleAssignmentType = v1beta1.AuthDynamicRoleAssignmentType

const (
	RoleAssignmentTypeStatic  = v1beta1.AuthStaticRoleAssignmentTypeStatic
	RoleAssignmentTypeDynamic = v1beta1.AuthDynamicRoleAssignmentTypeDynamic

	// Direct aliases for compatibility
	AuthStaticRoleAssignmentTypeStatic   = v1beta1.AuthStaticRoleAssignmentTypeStatic
	AuthDynamicRoleAssignmentTypeDynamic = v1beta1.AuthDynamicRoleAssignmentTypeDynamic
)

// ========== Organization Assignment Types ==========

type AuthOrganizationAssignment = v1beta1.AuthOrganizationAssignment
type AuthStaticOrganizationAssignment = v1beta1.AuthStaticOrganizationAssignment
type AuthStaticOrganizationAssignmentType = v1beta1.AuthStaticOrganizationAssignmentType
type AuthDynamicOrganizationAssignment = v1beta1.AuthDynamicOrganizationAssignment
type AuthDynamicOrganizationAssignmentType = v1beta1.AuthDynamicOrganizationAssignmentType
type AuthPerUserOrganizationAssignment = v1beta1.AuthPerUserOrganizationAssignment
type AuthPerUserOrganizationAssignmentType = v1beta1.AuthPerUserOrganizationAssignmentType

const (
	OrgAssignmentTypeStatic  = v1beta1.AuthStaticOrganizationAssignmentTypeStatic
	OrgAssignmentTypeDynamic = v1beta1.AuthDynamicOrganizationAssignmentTypeDynamic
	OrgAssignmentTypePerUser = v1beta1.PerUser

	// Direct aliases for compatibility
	AuthStaticOrganizationAssignmentTypeStatic   = v1beta1.AuthStaticOrganizationAssignmentTypeStatic
	AuthDynamicOrganizationAssignmentTypeDynamic = v1beta1.AuthDynamicOrganizationAssignmentTypeDynamic
	PerUser                                      = v1beta1.PerUser
)

// ========== Introspection Types ==========

type OAuth2Introspection = v1beta1.OAuth2Introspection
type JwtIntrospectionSpec = v1beta1.JwtIntrospectionSpec
type JwtIntrospectionSpecType = v1beta1.JwtIntrospectionSpecType
type Rfc7662IntrospectionSpec = v1beta1.Rfc7662IntrospectionSpec
type Rfc7662IntrospectionSpecType = v1beta1.Rfc7662IntrospectionSpecType
type GitHubIntrospectionSpec = v1beta1.GitHubIntrospectionSpec
type GitHubIntrospectionSpecType = v1beta1.GitHubIntrospectionSpecType

const (
	IntrospectionTypeJwt     = v1beta1.Jwt
	IntrospectionTypeRfc7662 = v1beta1.Rfc7662
	IntrospectionTypeGithub  = v1beta1.Github
)

// ========== Token Types ==========

type TokenRequest = v1beta1.TokenRequest
type TokenRequestGrantType = v1beta1.TokenRequestGrantType
type TokenResponse = v1beta1.TokenResponse
type TokenResponseTokenType = v1beta1.TokenResponseTokenType
type UserInfoResponse = v1beta1.UserInfoResponse

const (
	TokenGrantTypeAuthorizationCode = v1beta1.AuthorizationCode
	TokenGrantTypeRefreshToken      = v1beta1.RefreshToken
	TokenTypeBearer                 = v1beta1.Bearer

	// Direct aliases for compatibility
	AuthorizationCode = v1beta1.AuthorizationCode
	RefreshToken      = v1beta1.RefreshToken
	Bearer            = v1beta1.Bearer
)

// ========== Utility Functions ==========

var InferOAuth2IntrospectionConfig = v1beta1.InferOAuth2IntrospectionConfig
