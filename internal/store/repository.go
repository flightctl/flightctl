package store

// CountByOrgResult holds the result of the group by query
// for organization.
type CountByOrgResult struct {
	OrgID string
	Count int64
}
