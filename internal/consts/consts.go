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
	DelayDeviceRenderCtxKey    ctxKey = "delay-device-render"
	EventSourceComponentCtxKey ctxKey = "event-source"
	EventActorCtxKey           ctxKey = "event-actor"
	TLSPeerCertificateCtxKey   ctxKey = "tls-peer-certificate"
	OrganizationIDCtxKey       ctxKey = "organization-id"
	AgentCtxKey                ctxKey = "agent"
	TokenCtxKey                ctxKey = "token"
	IdentityCtxKey             ctxKey = "identity"
	MappedIdentityCtxKey       ctxKey = "mapped-identity"

	// Session
	SessionIDCtxKey     ctxKey = "session-id"
	SessionCookieCtxKey ctxKey = "session-cookie"
	AuthorizationCtxKey ctxKey = "authorization"
	FormUsernameCtxKey  ctxKey = "form-username"
	FormPasswordCtxKey  ctxKey = "form-password"
)
