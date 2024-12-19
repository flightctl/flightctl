package queues

//go:generate go run -modfile=../../tools/go.mod go.uber.org/mock/mockgen -source=provider.go -destination=mock_provider.go -package=queues
