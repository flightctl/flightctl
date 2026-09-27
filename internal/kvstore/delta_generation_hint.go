package kvstore

import "time"

const DeltaGenerationHintTTL = 15 * time.Minute

// DeltaGenerationHint is cached only after a generation has succeeded.
type DeltaGenerationHint struct {
	DeltaRef  string `json:"deltaRef"`
	SizeBytes *int64 `json:"sizeBytes,omitempty"`
}
