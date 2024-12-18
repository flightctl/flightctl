package tasks

//go:generate go run -modfile=../../tools/go.mod go.uber.org/mock/mockgen -source=callback_manager.go -destination=../../internal/tasks/mock_callback_manager.go -package=tasks
