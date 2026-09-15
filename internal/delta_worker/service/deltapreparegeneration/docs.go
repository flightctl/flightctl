package deltapreparegeneration

//go:generate go run -modfile=../../../../tools/go.mod go.uber.org/mock/mockgen -source=service.go -destination=mock.go -package=deltapreparegeneration
//go:generate go run -modfile=../../../../tools/go.mod github.com/hexdigest/gowrap/cmd/gowrap gen -g -p . -i Service -t ../../../../internal/service/templates/service-tracing -o traced.gen.go -v TracerName=flightctl/delta-worker/service/deltapreparegeneration
