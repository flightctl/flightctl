package consts

type ctxKey string

const (
	// GRPC
	GrpcSessionIDKey        = "session-id"
	GrpcClientNameKey       = "client-name"
	GrpcSelectedProtocolKey = "selected-protocol"

	// Tasks
	TaskQueue = "task-queue"

	// Ctx
	InternalRequestCtxKey      ctxKey = "internal_request"
	DelayDeviceRenderCtxKey    ctxKey = "delayDeviceRender"
	EventSourceComponentCtxKey ctxKey = "event_source"
	EventActorCtxKey           ctxKey = "event_actor"
)
