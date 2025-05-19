package k8sclient

//go:generate go run -modfile=../../tools/go.mod go.uber.org/mock/mockgen -source=k8s_client.go -destination=mock_k8s_client.go -package=k8sclient
