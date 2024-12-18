package grpc_v1

//go:generate go install -mod=readonly -modfile=../../../tools/go.mod google.golang.org/protobuf/cmd/protoc-gen-go
//go:generate go install -mod=readonly -modfile=../../../tools/go.mod google.golang.org/grpc/cmd/protoc-gen-go-grpc
//go:generate env PATH=../../../bin:$PATH protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative router.proto
//go:generate go run -modfile=../../../tools/go.mod go.uber.org/mock/mockgen -source=router_grpc.pb.go -destination=../../../internal/agent/device/console/mock_router_service_client.go -package=console
