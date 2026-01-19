package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// ========== Application Types ==========

type ApplicationProviderSpec = v1beta1.ApplicationProviderSpec

type ComposeApplication = v1beta1.ComposeApplication
type QuadletApplication = v1beta1.QuadletApplication
type ContainerApplication = v1beta1.ContainerApplication
type HelmApplication = v1beta1.HelmApplication
type ImageApplicationProviderSpec = v1beta1.ImageApplicationProviderSpec
type InlineApplicationProviderSpec = v1beta1.InlineApplicationProviderSpec

// ApplicationProviderType discriminator type
type ApplicationProviderType = v1beta1.ApplicationProviderType

const (
	ImageApplicationProviderType  = v1beta1.ImageApplicationProviderType
	InlineApplicationProviderType = v1beta1.InlineApplicationProviderType
)

// ========== Application Content ==========

type ApplicationContent = v1beta1.ApplicationContent
type ApplicationEnvVars = v1beta1.ApplicationEnvVars
type ApplicationPort = v1beta1.ApplicationPort
type ApplicationResources = v1beta1.ApplicationResources
type ApplicationResourceLimits = v1beta1.ApplicationResourceLimits

// ========== Application Volume Types ==========

type ApplicationVolume = v1beta1.ApplicationVolume
type ApplicationVolumeProviderSpec = v1beta1.ApplicationVolumeProviderSpec
type ApplicationVolumeReclaimPolicy = v1beta1.ApplicationVolumeReclaimPolicy
type ApplicationVolumeStatus = v1beta1.ApplicationVolumeStatus
type ImageVolumeProviderSpec = v1beta1.ImageVolumeProviderSpec
type ImageVolumeSource = v1beta1.ImageVolumeSource
type ImageMountVolumeProviderSpec = v1beta1.ImageMountVolumeProviderSpec
type MountVolumeProviderSpec = v1beta1.MountVolumeProviderSpec
type VolumeMount = v1beta1.VolumeMount

// ApplicationVolumeProviderType discriminator type
type ApplicationVolumeProviderType = v1beta1.ApplicationVolumeProviderType

const (
	ImageApplicationVolumeProviderType      = v1beta1.ImageApplicationVolumeProviderType
	MountApplicationVolumeProviderType      = v1beta1.MountApplicationVolumeProviderType
	ImageMountApplicationVolumeProviderType = v1beta1.ImageMountApplicationVolumeProviderType
)

const (
	ApplicationVolumeReclaimPolicyRetain = v1beta1.Retain

	// Direct alias for compatibility
	Retain = v1beta1.Retain
)

// ========== Application Status ==========

type ApplicationStatusType = v1beta1.ApplicationStatusType
type ApplicationsSummaryStatusType = v1beta1.ApplicationsSummaryStatusType

const (
	ApplicationStatusCompleted = v1beta1.ApplicationStatusCompleted
	ApplicationStatusError     = v1beta1.ApplicationStatusError
	ApplicationStatusPreparing = v1beta1.ApplicationStatusPreparing
	ApplicationStatusRunning   = v1beta1.ApplicationStatusRunning
	ApplicationStatusStarting  = v1beta1.ApplicationStatusStarting
	ApplicationStatusUnknown   = v1beta1.ApplicationStatusUnknown
)

const (
	ApplicationsSummaryStatusDegraded       = v1beta1.ApplicationsSummaryStatusDegraded
	ApplicationsSummaryStatusError          = v1beta1.ApplicationsSummaryStatusError
	ApplicationsSummaryStatusHealthy        = v1beta1.ApplicationsSummaryStatusHealthy
	ApplicationsSummaryStatusNoApplications = v1beta1.ApplicationsSummaryStatusNoApplications
	ApplicationsSummaryStatusUnknown        = v1beta1.ApplicationsSummaryStatusUnknown
)

// ========== App Type ==========

type AppType = v1beta1.AppType

const (
	AppTypeCompose   = v1beta1.AppTypeCompose
	AppTypeContainer = v1beta1.AppTypeContainer
	AppTypeHelm      = v1beta1.AppTypeHelm
	AppTypeQuadlet   = v1beta1.AppTypeQuadlet
)

// ========== Image Pull Policy ==========

type ImagePullPolicy = v1beta1.ImagePullPolicy

const (
	ImagePullPolicyAlways       = v1beta1.PullAlways
	ImagePullPolicyIfNotPresent = v1beta1.PullIfNotPresent
	ImagePullPolicyNever        = v1beta1.PullNever

	// Direct aliases for compatibility
	PullAlways       = v1beta1.PullAlways
	PullIfNotPresent = v1beta1.PullIfNotPresent
	PullNever        = v1beta1.PullNever
)
