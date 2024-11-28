// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             v3.19.6
// source: api/grpc/v1/agent.proto

package grpc_v1

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// EnrollmentServiceClient is the client API for EnrollmentService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type EnrollmentServiceClient interface {
	// RequestEnrollment enrolls a device with the service.
	//
	// Parameters:
	// - EnrollmentRequest: Contains device enrollment information.
	//
	// Returns:
	// - EnrollmentResponse: Confirmation of enrollment.
	//
	// Errors:
	// - INVALID_ARGUMENT: The request is invalid.
	// - AUTHENTICATION_FAILED: The provided certificate is not valid for performing an enrollment request.
	RequestEnrollment(ctx context.Context, in *EnrollmentRequest, opts ...grpc.CallOption) (*EnrollmentResponse, error)
	// GetEnrollment retrieves enrollment status and details for a device.
	//
	// Parameters:
	// - GetEnrollmentRequest: Includes the device name.
	//
	// Returns:
	// - GetEnrollmentResponse: Contains the enrollment status and details.
	//
	// Errors:
	// - NOT_FOUND: If the enrollment request does not exist.
	// - INVALID_ARGUMENT: If the request is invalid.
	// - AUTHENTICATION_FAILED: If the provided certificate is not valid for enrollment request phase.
	GetEnrollment(ctx context.Context, in *GetEnrollmentRequest, opts ...grpc.CallOption) (*GetEnrollmentResponse, error)
}

type enrollmentServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewEnrollmentServiceClient(cc grpc.ClientConnInterface) EnrollmentServiceClient {
	return &enrollmentServiceClient{cc}
}

func (c *enrollmentServiceClient) RequestEnrollment(ctx context.Context, in *EnrollmentRequest, opts ...grpc.CallOption) (*EnrollmentResponse, error) {
	out := new(EnrollmentResponse)
	err := c.cc.Invoke(ctx, "/flightctl.v1.EnrollmentService/RequestEnrollment", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *enrollmentServiceClient) GetEnrollment(ctx context.Context, in *GetEnrollmentRequest, opts ...grpc.CallOption) (*GetEnrollmentResponse, error) {
	out := new(GetEnrollmentResponse)
	err := c.cc.Invoke(ctx, "/flightctl.v1.EnrollmentService/GetEnrollment", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnrollmentServiceServer is the server API for EnrollmentService service.
// All implementations must embed UnimplementedEnrollmentServiceServer
// for forward compatibility
type EnrollmentServiceServer interface {
	// RequestEnrollment enrolls a device with the service.
	//
	// Parameters:
	// - EnrollmentRequest: Contains device enrollment information.
	//
	// Returns:
	// - EnrollmentResponse: Confirmation of enrollment.
	//
	// Errors:
	// - INVALID_ARGUMENT: The request is invalid.
	// - AUTHENTICATION_FAILED: The provided certificate is not valid for performing an enrollment request.
	RequestEnrollment(context.Context, *EnrollmentRequest) (*EnrollmentResponse, error)
	// GetEnrollment retrieves enrollment status and details for a device.
	//
	// Parameters:
	// - GetEnrollmentRequest: Includes the device name.
	//
	// Returns:
	// - GetEnrollmentResponse: Contains the enrollment status and details.
	//
	// Errors:
	// - NOT_FOUND: If the enrollment request does not exist.
	// - INVALID_ARGUMENT: If the request is invalid.
	// - AUTHENTICATION_FAILED: If the provided certificate is not valid for enrollment request phase.
	GetEnrollment(context.Context, *GetEnrollmentRequest) (*GetEnrollmentResponse, error)
	mustEmbedUnimplementedEnrollmentServiceServer()
}

// UnimplementedEnrollmentServiceServer must be embedded to have forward compatible implementations.
type UnimplementedEnrollmentServiceServer struct {
}

func (UnimplementedEnrollmentServiceServer) RequestEnrollment(context.Context, *EnrollmentRequest) (*EnrollmentResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RequestEnrollment not implemented")
}
func (UnimplementedEnrollmentServiceServer) GetEnrollment(context.Context, *GetEnrollmentRequest) (*GetEnrollmentResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetEnrollment not implemented")
}
func (UnimplementedEnrollmentServiceServer) mustEmbedUnimplementedEnrollmentServiceServer() {}

// UnsafeEnrollmentServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to EnrollmentServiceServer will
// result in compilation errors.
type UnsafeEnrollmentServiceServer interface {
	mustEmbedUnimplementedEnrollmentServiceServer()
}

func RegisterEnrollmentServiceServer(s grpc.ServiceRegistrar, srv EnrollmentServiceServer) {
	s.RegisterService(&EnrollmentService_ServiceDesc, srv)
}

func _EnrollmentService_RequestEnrollment_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(EnrollmentRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(EnrollmentServiceServer).RequestEnrollment(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/flightctl.v1.EnrollmentService/RequestEnrollment",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(EnrollmentServiceServer).RequestEnrollment(ctx, req.(*EnrollmentRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _EnrollmentService_GetEnrollment_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetEnrollmentRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(EnrollmentServiceServer).GetEnrollment(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/flightctl.v1.EnrollmentService/GetEnrollment",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(EnrollmentServiceServer).GetEnrollment(ctx, req.(*GetEnrollmentRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// EnrollmentService_ServiceDesc is the grpc.ServiceDesc for EnrollmentService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var EnrollmentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "flightctl.v1.EnrollmentService",
	HandlerType: (*EnrollmentServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "RequestEnrollment",
			Handler:    _EnrollmentService_RequestEnrollment_Handler,
		},
		{
			MethodName: "GetEnrollment",
			Handler:    _EnrollmentService_GetEnrollment_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "api/grpc/v1/agent.proto",
}

// HealthCheckServiceClient is the client API for HealthCheckService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type HealthCheckServiceClient interface {
	Heartbeat(ctx context.Context, in *HeartBeatRequest, opts ...grpc.CallOption) (*HeartBeatResponse, error)
}

type healthCheckServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewHealthCheckServiceClient(cc grpc.ClientConnInterface) HealthCheckServiceClient {
	return &healthCheckServiceClient{cc}
}

func (c *healthCheckServiceClient) Heartbeat(ctx context.Context, in *HeartBeatRequest, opts ...grpc.CallOption) (*HeartBeatResponse, error) {
	out := new(HeartBeatResponse)
	err := c.cc.Invoke(ctx, "/flightctl.v1.HealthCheckService/Heartbeat", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HealthCheckServiceServer is the server API for HealthCheckService service.
// All implementations must embed UnimplementedHealthCheckServiceServer
// for forward compatibility
type HealthCheckServiceServer interface {
	Heartbeat(context.Context, *HeartBeatRequest) (*HeartBeatResponse, error)
	mustEmbedUnimplementedHealthCheckServiceServer()
}

// UnimplementedHealthCheckServiceServer must be embedded to have forward compatible implementations.
type UnimplementedHealthCheckServiceServer struct {
}

func (UnimplementedHealthCheckServiceServer) Heartbeat(context.Context, *HeartBeatRequest) (*HeartBeatResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Heartbeat not implemented")
}
func (UnimplementedHealthCheckServiceServer) mustEmbedUnimplementedHealthCheckServiceServer() {}

// UnsafeHealthCheckServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to HealthCheckServiceServer will
// result in compilation errors.
type UnsafeHealthCheckServiceServer interface {
	mustEmbedUnimplementedHealthCheckServiceServer()
}

func RegisterHealthCheckServiceServer(s grpc.ServiceRegistrar, srv HealthCheckServiceServer) {
	s.RegisterService(&HealthCheckService_ServiceDesc, srv)
}

func _HealthCheckService_Heartbeat_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(HeartBeatRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(HealthCheckServiceServer).Heartbeat(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/flightctl.v1.HealthCheckService/Heartbeat",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(HealthCheckServiceServer).Heartbeat(ctx, req.(*HeartBeatRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// HealthCheckService_ServiceDesc is the grpc.ServiceDesc for HealthCheckService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var HealthCheckService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "flightctl.v1.HealthCheckService",
	HandlerType: (*HealthCheckServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Heartbeat",
			Handler:    _HealthCheckService_Heartbeat_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "api/grpc/v1/agent.proto",
}

// AgentServiceClient is the client API for AgentService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type AgentServiceClient interface {
	// GetRenderedSpec retrieves the specification for a device.
	//
	// Parameters:
	// - SpecRequest: Includes the device name and known version.
	//
	// Returns:
	// - SpecResponse: Contains the device specification.
	//
	// Errors:
	// - NOT_FOUND: If the device does not exist.
	// - INVALID_ARGUMENT: If the request is invalid.
	// - AUTHENTICATION_FAILED: If the provided certificate is not valid for the specific device.
	GetRenderedSpec(ctx context.Context, in *GetRenderedSpecRequest, opts ...grpc.CallOption) (*GetRenderedSpecResponse, error)
	// UpdateStatus updates the status of a device.
	//
	// Parameters:
	// - UpdateStatusRequest: Contains the new status information.
	//
	// Returns:
	// - UpdateStatusResponse: Acknowledgment of the update.
	//
	// Errors:
	// - INVALID_ARGUMENT: If the status information is invalid.
	UpdateStatus(ctx context.Context, in *UpdateStatusRequest, opts ...grpc.CallOption) (*UpdateStatusResponse, error)
}

type agentServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewAgentServiceClient(cc grpc.ClientConnInterface) AgentServiceClient {
	return &agentServiceClient{cc}
}

func (c *agentServiceClient) GetRenderedSpec(ctx context.Context, in *GetRenderedSpecRequest, opts ...grpc.CallOption) (*GetRenderedSpecResponse, error) {
	out := new(GetRenderedSpecResponse)
	err := c.cc.Invoke(ctx, "/flightctl.v1.AgentService/GetRenderedSpec", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *agentServiceClient) UpdateStatus(ctx context.Context, in *UpdateStatusRequest, opts ...grpc.CallOption) (*UpdateStatusResponse, error) {
	out := new(UpdateStatusResponse)
	err := c.cc.Invoke(ctx, "/flightctl.v1.AgentService/UpdateStatus", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AgentServiceServer is the server API for AgentService service.
// All implementations must embed UnimplementedAgentServiceServer
// for forward compatibility
type AgentServiceServer interface {
	// GetRenderedSpec retrieves the specification for a device.
	//
	// Parameters:
	// - SpecRequest: Includes the device name and known version.
	//
	// Returns:
	// - SpecResponse: Contains the device specification.
	//
	// Errors:
	// - NOT_FOUND: If the device does not exist.
	// - INVALID_ARGUMENT: If the request is invalid.
	// - AUTHENTICATION_FAILED: If the provided certificate is not valid for the specific device.
	GetRenderedSpec(context.Context, *GetRenderedSpecRequest) (*GetRenderedSpecResponse, error)
	// UpdateStatus updates the status of a device.
	//
	// Parameters:
	// - UpdateStatusRequest: Contains the new status information.
	//
	// Returns:
	// - UpdateStatusResponse: Acknowledgment of the update.
	//
	// Errors:
	// - INVALID_ARGUMENT: If the status information is invalid.
	UpdateStatus(context.Context, *UpdateStatusRequest) (*UpdateStatusResponse, error)
	mustEmbedUnimplementedAgentServiceServer()
}

// UnimplementedAgentServiceServer must be embedded to have forward compatible implementations.
type UnimplementedAgentServiceServer struct {
}

func (UnimplementedAgentServiceServer) GetRenderedSpec(context.Context, *GetRenderedSpecRequest) (*GetRenderedSpecResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetRenderedSpec not implemented")
}
func (UnimplementedAgentServiceServer) UpdateStatus(context.Context, *UpdateStatusRequest) (*UpdateStatusResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateStatus not implemented")
}
func (UnimplementedAgentServiceServer) mustEmbedUnimplementedAgentServiceServer() {}

// UnsafeAgentServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to AgentServiceServer will
// result in compilation errors.
type UnsafeAgentServiceServer interface {
	mustEmbedUnimplementedAgentServiceServer()
}

func RegisterAgentServiceServer(s grpc.ServiceRegistrar, srv AgentServiceServer) {
	s.RegisterService(&AgentService_ServiceDesc, srv)
}

func _AgentService_GetRenderedSpec_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetRenderedSpecRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).GetRenderedSpec(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/flightctl.v1.AgentService/GetRenderedSpec",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AgentServiceServer).GetRenderedSpec(ctx, req.(*GetRenderedSpecRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AgentService_UpdateStatus_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdateStatusRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).UpdateStatus(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/flightctl.v1.AgentService/UpdateStatus",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AgentServiceServer).UpdateStatus(ctx, req.(*UpdateStatusRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// AgentService_ServiceDesc is the grpc.ServiceDesc for AgentService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var AgentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "flightctl.v1.AgentService",
	HandlerType: (*AgentServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetRenderedSpec",
			Handler:    _AgentService_GetRenderedSpec_Handler,
		},
		{
			MethodName: "UpdateStatus",
			Handler:    _AgentService_UpdateStatus_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "api/grpc/v1/agent.proto",
}

// RouterServiceClient is the client API for RouterService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type RouterServiceClient interface {
	// Stream connects caller to another caller of the same stream, this is used
	// to connect two endpoints together, provide console access, or general TCP proxying.
	//
	// Parameters:
	// - StreamRequest: Contains the payload. (stream)
	//
	// Returns:
	// - StreamResponse: Contains the payload. (stream)
	//
	// Errors:
	// - INVALID_ARGUMENT: If the stream ID is invalid.
	//
	// Metadata:
	// - stream-id: The ID of the stream.
	Stream(ctx context.Context, opts ...grpc.CallOption) (RouterService_StreamClient, error)
}

type routerServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewRouterServiceClient(cc grpc.ClientConnInterface) RouterServiceClient {
	return &routerServiceClient{cc}
}

func (c *routerServiceClient) Stream(ctx context.Context, opts ...grpc.CallOption) (RouterService_StreamClient, error) {
	stream, err := c.cc.NewStream(ctx, &RouterService_ServiceDesc.Streams[0], "/flightctl.v1.RouterService/Stream", opts...)
	if err != nil {
		return nil, err
	}
	x := &routerServiceStreamClient{stream}
	return x, nil
}

type RouterService_StreamClient interface {
	Send(*StreamRequest) error
	Recv() (*StreamResponse, error)
	grpc.ClientStream
}

type routerServiceStreamClient struct {
	grpc.ClientStream
}

func (x *routerServiceStreamClient) Send(m *StreamRequest) error {
	return x.ClientStream.SendMsg(m)
}

func (x *routerServiceStreamClient) Recv() (*StreamResponse, error) {
	m := new(StreamResponse)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// RouterServiceServer is the server API for RouterService service.
// All implementations must embed UnimplementedRouterServiceServer
// for forward compatibility
type RouterServiceServer interface {
	// Stream connects caller to another caller of the same stream, this is used
	// to connect two endpoints together, provide console access, or general TCP proxying.
	//
	// Parameters:
	// - StreamRequest: Contains the payload. (stream)
	//
	// Returns:
	// - StreamResponse: Contains the payload. (stream)
	//
	// Errors:
	// - INVALID_ARGUMENT: If the stream ID is invalid.
	//
	// Metadata:
	// - stream-id: The ID of the stream.
	Stream(RouterService_StreamServer) error
	mustEmbedUnimplementedRouterServiceServer()
}

// UnimplementedRouterServiceServer must be embedded to have forward compatible implementations.
type UnimplementedRouterServiceServer struct {
}

func (UnimplementedRouterServiceServer) Stream(RouterService_StreamServer) error {
	return status.Errorf(codes.Unimplemented, "method Stream not implemented")
}
func (UnimplementedRouterServiceServer) mustEmbedUnimplementedRouterServiceServer() {}

// UnsafeRouterServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to RouterServiceServer will
// result in compilation errors.
type UnsafeRouterServiceServer interface {
	mustEmbedUnimplementedRouterServiceServer()
}

func RegisterRouterServiceServer(s grpc.ServiceRegistrar, srv RouterServiceServer) {
	s.RegisterService(&RouterService_ServiceDesc, srv)
}

func _RouterService_Stream_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(RouterServiceServer).Stream(&routerServiceStreamServer{stream})
}

type RouterService_StreamServer interface {
	Send(*StreamResponse) error
	Recv() (*StreamRequest, error)
	grpc.ServerStream
}

type routerServiceStreamServer struct {
	grpc.ServerStream
}

func (x *routerServiceStreamServer) Send(m *StreamResponse) error {
	return x.ServerStream.SendMsg(m)
}

func (x *routerServiceStreamServer) Recv() (*StreamRequest, error) {
	m := new(StreamRequest)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// RouterService_ServiceDesc is the grpc.ServiceDesc for RouterService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var RouterService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "flightctl.v1.RouterService",
	HandlerType: (*RouterServiceServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Stream",
			Handler:       _RouterService_Stream_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "api/grpc/v1/agent.proto",
}
