package helm

import (
	"fmt"

	"github.com/samber/lo"
)

func AppNamespace(namespace *string, appName string) string {
	if lo.FromPtr(namespace) != "" {
		return *namespace
	}
	return fmt.Sprintf("%s-%s", "flightctl", appName)
}
