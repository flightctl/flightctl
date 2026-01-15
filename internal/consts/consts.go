package consts

type ctxKey string

// UpdateState represents the state of a device update.
type UpdateState string

const (
	// The agent is validating the desired device spec and downloading
	// dependencies. No changes have been made to the device's configuration
	// yet.
	UpdateStatePreparing UpdateState = "Preparing"
	//  The agent has validated the desired spec, downloaded all dependencies,
	//  and is ready to update. No changes have been made to the device's
	//  configuration yet.
	UpdateStateReadyToUpdate UpdateState = "ReadyToUpdate"
	// The agent has started the update transaction and is writing the update to
	// disk.
	UpdateStateApplyingUpdate UpdateState = "ApplyingUpdate"
	// The agent initiated a reboot required to activate the new OS image and configuration.
	UpdateStateRebooting UpdateState = "Rebooting"
	// The agent has successfully completed the update and the device is
	// conforming to its device spec. Note that the device's update status may
	// still be reported as `OutOfDate` if the device spec is not yet at the
	// same version as the fleet's device template
	UpdateStateUpdated UpdateState = "Updated"
	// The agent has canceled the update because the desired spec was reverted
	// to the current spec before the update process started.
	UpdateStateCanceled UpdateState = "Canceled"
	// The agent failed to apply the desired spec and will not retry. The
	// device's OS image and configuration have been rolled back to the
	// pre-update version and have been activated
	UpdateStateError UpdateState = "Error"
	// The agent has detected an error and is rolling back to the pre-update OS
	// image and configuration.
	UpdateStateRollingBack UpdateState = "RollingBack"
	// The agent failed to apply the desired spec and will retry. The device's
	// OS image and configuration have been rolled back to the pre-update
	// version and have been activated.
	UpdateStateRetrying UpdateState = "Retrying"
)

const (
	// GRPC
	GrpcSessionIDKey        = "session-id"
	GrpcClientNameKey       = "client-name"
	GrpcSelectedProtocolKey = "selected-protocol"

	// Tasks
	TaskQueue           = "task-queue"
	ImageBuildTaskQueue = "imagebuild-queue"

	// Checkpoints
	CheckpointConsumerEventProcessor = "event_processor"
	CheckpointConsumerTaskQueue      = "task_queue"
	CheckpointKeyGlobal              = "global_checkpoint"

	// Ctx
	InternalRequestCtxKey      ctxKey = "internal-request"
	ResourceSyncRequestCtxKey  ctxKey = "resource-sync-request"
	DelayDeviceRenderCtxKey    ctxKey = "delay-device-render"
	EventSourceComponentCtxKey ctxKey = "event-source"
	EventActorCtxKey           ctxKey = "event-actor"
	TLSPeerCertificateCtxKey   ctxKey = "tls-peer-certificate"
	OrganizationIDCtxKey       ctxKey = "organization-id"
	UserAgentCtxKey            ctxKey = "user-agent"
	AgentCtxKey                ctxKey = "agent"
	TokenCtxKey                ctxKey = "token"
	IdentityCtxKey             ctxKey = "identity"
	MappedIdentityCtxKey       ctxKey = "mapped-identity"
)
