package worker_client

//go:generate go run -modfile=../../tools/go.mod go.uber.org/mock/mockgen -source=worker_client.go -destination=mock_worker_client.go -package=worker_client
