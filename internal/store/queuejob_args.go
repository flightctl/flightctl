package store

//go:generate mockgen --build_flags=--mod=mod -destination mock_riverclient.go -prog_only -imports github.com/jackc/pgx/v5 -package store github.com/riverqueue/river Client[pgx.Tx]

type TestRepoArgs struct{}

func (TestRepoArgs) Kind() string { return "test-repository" }

type FleetTemplateUpdateArgs struct {
	OrgID string `json:"orgId"`
	Name  string `json:"name"`
}

func (FleetTemplateUpdateArgs) Kind() string { return "fleet-template-update" }
