package store

import (
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/selector"
)

var (
	NullOrgId              = org.DefaultID
	CurrentContinueVersion = 1
)

type SortColumn string
type SortOrder string

const (
	SortByName      SortColumn = "name"
	SortByCreatedAt SortColumn = "created_at"

	// Sort columns for vulnerability queries.
	SortByOwner          SortColumn = "owner"
	SortByDeviceAffected SortColumn = "device_affected"
	SortBySeverity       SortColumn = "severity"
	SortByCvssScore      SortColumn = "cvss_score"
	SortByPublishedAt    SortColumn = "published_at"
	SortByCveId          SortColumn = "cve_id"

	SortAsc  SortOrder = "asc"
	SortDesc SortOrder = "desc"
)

type ListParams struct {
	Limit              int
	Continue           *Continue
	FieldSelector      *selector.FieldSelector
	LabelSelector      *selector.LabelSelector
	AnnotationSelector *selector.AnnotationSelector
	SortOrder          *SortOrder
	SortColumns        []SortColumn
}

type Continue struct {
	Version int
	Names   []string
	Count   int64
}

func BuildContinueString(names []string, count int64) *string {
	cont := Continue{
		Version: CurrentContinueVersion,
		Names:   names,
		Count:   count,
	}

	sEnc, _ := json.Marshal(cont)
	sEncStr := b64.StdEncoding.EncodeToString(sEnc)
	return &sEncStr
}

func ParseContinueString(contStr *string) (*Continue, error) {
	var cont Continue

	if contStr == nil {
		return nil, nil
	}

	sDec, err := b64.StdEncoding.DecodeString(*contStr)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(sDec, &cont); err != nil {
		return nil, err
	}
	if cont.Version != CurrentContinueVersion {
		return nil, fmt.Errorf("continue string version %d must be %d", cont.Version, CurrentContinueVersion)
	}

	return &cont, nil
}
