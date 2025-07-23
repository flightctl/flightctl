package consts

type ctxKey string

const (
	// GRPC
	GrpcSessionIDKey        = "session-id"
	GrpcClientNameKey       = "client-name"
	GrpcSelectedProtocolKey = "selected-protocol"

	// Tasks
	TaskQueue         = "task-queue"
	PeriodicTaskQueue = "periodic-task-queue"

	// Ctx
	InternalRequestCtxKey      ctxKey = "internal-request"
	DelayDeviceRenderCtxKey    ctxKey = "delay-device-render"
	EventSourceComponentCtxKey ctxKey = "event-source"
	EventActorCtxKey           ctxKey = "event-actor"
	TLSPeerCertificateCtxKey   ctxKey = "tls-peer-certificate"
	OrganizationIDCtxKey       ctxKey = "organization-id"
)
