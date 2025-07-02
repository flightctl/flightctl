package tasks_client

//go:generate go run -modfile=../../tools/go.mod go.uber.org/mock/mockgen -source=callback_manager.go -destination=mock_callback_manager.go -package=tasks_client
