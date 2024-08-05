package spec

import (
	"fmt"
	"strconv"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
)

// Rendered represents the rendered device spec with an additional rollback field.
type Rendered struct {
	*v1alpha1.RenderedDeviceSpec
	Rollback bool `json:"rollback"`
}

// NewRendered creates a new rendered device spec.
func NewRendered() *Rendered {
	return &Rendered{
		RenderedDeviceSpec: &v1alpha1.RenderedDeviceSpec{},
	}
}

func (r *Rendered) SetRollback(rollback bool) {
	r.Rollback = rollback
}

func (r *Rendered) IsRollback() bool {
	return r.Rollback
}

// GetVersion returns the rendered version. If the spec is marked as a rollback,
// the next version is returned to ensure the
func (r *Rendered) GetVersion() (string, error) {
	currentVersion := r.RenderedVersion
	if !r.IsRollback() {
		return r.RenderedVersion, nil
	}
	versionNum, err := strconv.Atoi(currentVersion)
	if err != nil {
		return "", fmt.Errorf("failed to convert version to integer: %v", err)
	}

	nextVersionNum := versionNum + 1
	nextVersion := strconv.Itoa(nextVersionNum)
	return nextVersion, nil
}
