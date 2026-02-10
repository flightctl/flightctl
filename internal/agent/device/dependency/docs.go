package dependency

//go:generate go run -modfile=../../../../tools/go.mod go.uber.org/mock/mockgen -source=dependency.go -destination=mock_dependency.go -package=dependency
//go:generate go run -modfile=../../../../tools/go.mod go.uber.org/mock/mockgen -source=pull_config.go -destination=mock_pull_config.go -package=dependency
