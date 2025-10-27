package audit

//go:generate go run -modfile=../../../../../tools/go.mod go.uber.org/mock/mockgen -source=interfaces.go -destination=mock_audit.go -package=audit

