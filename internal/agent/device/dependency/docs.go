package dependency

//go:generate go run -modfile=../../../../tools/go.mod go.uber.org/mock/mockgen -source=dependency.go -destination=mock_dependency.go -package=dependency
