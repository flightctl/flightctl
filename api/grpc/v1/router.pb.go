// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        v4.24.0--rc2
// source: api/grpc/v1/router.proto

package grpc_v1

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type StreamRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Payload []byte `protobuf:"bytes,1,opt,name=payload,proto3" json:"payload,omitempty"`
	Closed  bool   `protobuf:"varint,2,opt,name=closed,proto3" json:"closed,omitempty"`
}

func (x *StreamRequest) Reset() {
	*x = StreamRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_api_grpc_v1_router_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *StreamRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StreamRequest) ProtoMessage() {}

func (x *StreamRequest) ProtoReflect() protoreflect.Message {
	mi := &file_api_grpc_v1_router_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StreamRequest.ProtoReflect.Descriptor instead.
func (*StreamRequest) Descriptor() ([]byte, []int) {
	return file_api_grpc_v1_router_proto_rawDescGZIP(), []int{0}
}

func (x *StreamRequest) GetPayload() []byte {
	if x != nil {
		return x.Payload
	}
	return nil
}

func (x *StreamRequest) GetClosed() bool {
	if x != nil {
		return x.Closed
	}
	return false
}

type StreamResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Payload []byte `protobuf:"bytes,1,opt,name=payload,proto3" json:"payload,omitempty"`
	Closed  bool   `protobuf:"varint,2,opt,name=closed,proto3" json:"closed,omitempty"`
}

func (x *StreamResponse) Reset() {
	*x = StreamResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_api_grpc_v1_router_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *StreamResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StreamResponse) ProtoMessage() {}

func (x *StreamResponse) ProtoReflect() protoreflect.Message {
	mi := &file_api_grpc_v1_router_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StreamResponse.ProtoReflect.Descriptor instead.
func (*StreamResponse) Descriptor() ([]byte, []int) {
	return file_api_grpc_v1_router_proto_rawDescGZIP(), []int{1}
}

func (x *StreamResponse) GetPayload() []byte {
	if x != nil {
		return x.Payload
	}
	return nil
}

func (x *StreamResponse) GetClosed() bool {
	if x != nil {
		return x.Closed
	}
	return false
}

var File_api_grpc_v1_router_proto protoreflect.FileDescriptor

var file_api_grpc_v1_router_proto_rawDesc = []byte{
	0x0a, 0x18, 0x61, 0x70, 0x69, 0x2f, 0x67, 0x72, 0x70, 0x63, 0x2f, 0x76, 0x31, 0x2f, 0x72, 0x6f,
	0x75, 0x74, 0x65, 0x72, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x0c, 0x66, 0x6c, 0x69, 0x67,
	0x68, 0x74, 0x63, 0x74, 0x6c, 0x2e, 0x76, 0x31, 0x22, 0x41, 0x0a, 0x0d, 0x53, 0x74, 0x72, 0x65,
	0x61, 0x6d, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x18, 0x0a, 0x07, 0x70, 0x61, 0x79,
	0x6c, 0x6f, 0x61, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x07, 0x70, 0x61, 0x79, 0x6c,
	0x6f, 0x61, 0x64, 0x12, 0x16, 0x0a, 0x06, 0x63, 0x6c, 0x6f, 0x73, 0x65, 0x64, 0x18, 0x02, 0x20,
	0x01, 0x28, 0x08, 0x52, 0x06, 0x63, 0x6c, 0x6f, 0x73, 0x65, 0x64, 0x22, 0x42, 0x0a, 0x0e, 0x53,
	0x74, 0x72, 0x65, 0x61, 0x6d, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x18, 0x0a,
	0x07, 0x70, 0x61, 0x79, 0x6c, 0x6f, 0x61, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x07,
	0x70, 0x61, 0x79, 0x6c, 0x6f, 0x61, 0x64, 0x12, 0x16, 0x0a, 0x06, 0x63, 0x6c, 0x6f, 0x73, 0x65,
	0x64, 0x18, 0x02, 0x20, 0x01, 0x28, 0x08, 0x52, 0x06, 0x63, 0x6c, 0x6f, 0x73, 0x65, 0x64, 0x32,
	0x58, 0x0a, 0x0d, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x72, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65,
	0x12, 0x47, 0x0a, 0x06, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x12, 0x1b, 0x2e, 0x66, 0x6c, 0x69,
	0x67, 0x68, 0x74, 0x63, 0x74, 0x6c, 0x2e, 0x76, 0x31, 0x2e, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d,
	0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x1c, 0x2e, 0x66, 0x6c, 0x69, 0x67, 0x68, 0x74,
	0x63, 0x74, 0x6c, 0x2e, 0x76, 0x31, 0x2e, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x52, 0x65, 0x73,
	0x70, 0x6f, 0x6e, 0x73, 0x65, 0x28, 0x01, 0x30, 0x01, 0x42, 0x34, 0x5a, 0x32, 0x67, 0x69, 0x74,
	0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x66, 0x6c, 0x69, 0x67, 0x68, 0x74, 0x63, 0x74,
	0x6c, 0x2f, 0x66, 0x6c, 0x69, 0x67, 0x68, 0x74, 0x63, 0x74, 0x6c, 0x2f, 0x61, 0x70, 0x69, 0x2f,
	0x67, 0x72, 0x70, 0x63, 0x2f, 0x76, 0x31, 0x2f, 0x67, 0x72, 0x70, 0x63, 0x2d, 0x76, 0x31, 0x62,
	0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_api_grpc_v1_router_proto_rawDescOnce sync.Once
	file_api_grpc_v1_router_proto_rawDescData = file_api_grpc_v1_router_proto_rawDesc
)

func file_api_grpc_v1_router_proto_rawDescGZIP() []byte {
	file_api_grpc_v1_router_proto_rawDescOnce.Do(func() {
		file_api_grpc_v1_router_proto_rawDescData = protoimpl.X.CompressGZIP(file_api_grpc_v1_router_proto_rawDescData)
	})
	return file_api_grpc_v1_router_proto_rawDescData
}

var file_api_grpc_v1_router_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_api_grpc_v1_router_proto_goTypes = []interface{}{
	(*StreamRequest)(nil),  // 0: flightctl.v1.StreamRequest
	(*StreamResponse)(nil), // 1: flightctl.v1.StreamResponse
}
var file_api_grpc_v1_router_proto_depIdxs = []int32{
	0, // 0: flightctl.v1.RouterService.Stream:input_type -> flightctl.v1.StreamRequest
	1, // 1: flightctl.v1.RouterService.Stream:output_type -> flightctl.v1.StreamResponse
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_api_grpc_v1_router_proto_init() }
func file_api_grpc_v1_router_proto_init() {
	if File_api_grpc_v1_router_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_api_grpc_v1_router_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*StreamRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_api_grpc_v1_router_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*StreamResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_api_grpc_v1_router_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_api_grpc_v1_router_proto_goTypes,
		DependencyIndexes: file_api_grpc_v1_router_proto_depIdxs,
		MessageInfos:      file_api_grpc_v1_router_proto_msgTypes,
	}.Build()
	File_api_grpc_v1_router_proto = out.File
	file_api_grpc_v1_router_proto_rawDesc = nil
	file_api_grpc_v1_router_proto_goTypes = nil
	file_api_grpc_v1_router_proto_depIdxs = nil
}
