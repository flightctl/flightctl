package consts

type ctxKey string

const (
	// GRPC
	GrpcSessionIDKey        = "session-id"
	GrpcClientNameKey       = "client-name"
	GrpcSelectedProtocolKey = "selected-protocol"

	// Tasks
	TaskQueue = "task-queue"

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
