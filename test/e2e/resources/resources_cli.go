package resources

import (
	"fmt"
	"strings"
	"time"

	"encoding/json"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
)

const (
	Devices                           = "devices"
	Fleets                            = "fleets"
	Repositories                      = "repositories"
	get                               = "get"
	fieldSelectorSwitch               = "--field-selector"
	labelSelectorSwitch               = "-l"
	apply                             = "apply"
	wide                              = "wide"
	jsonStr                           = "json"
	UnknownOrUnsupportedSelectorError = "400, message: unknown or unsupported selector: unable to resolve selector name"
	FailedToResolveOperatorError      = "400, message: failed to parse field selector: failed to resolve operation for selector"
	InvalidFieldSelectorSyntax        = "400, message: failed to parse field selector: invalid field selector syntax"
)

func ListAll(harness *e2e.Harness, resourceKind string) (string, error) {
	return harness.CLI(get, resourceKind)
}

func ExpectNotExistWithName(harness *e2e.Harness, resourceKind string, name string) error {
	output, err := GetByName(harness, resourceKind, name)
	if err != nil && strings.Contains(output, "not found") {
		return nil
	}
	return err
}

func DevicesAreListed(harness *e2e.Harness, count int) error {
	listedDevices, err := ListAll(harness, Devices)
	return SomeRowsAreListedInResponse(listedDevices, err, count)
}

func FleetsAreListed(harness *e2e.Harness, count int) error {
	listedFleets, err := ListAll(harness, Fleets)
	return SomeRowsAreListedInResponse(listedFleets, err, count)
}

func RepositoriesAreListed(harness *e2e.Harness, count int) error {
	listedRepositories, err := ListAll(harness, Repositories)
	return SomeRowsAreListedInResponse(listedRepositories, err, count)
}

func GetByName(harness *e2e.Harness, resourceKind string, name string) (string, error) {
	return harness.CLI(get, fmt.Sprintf("%s/%s", resourceKind, name), "-o", wide)
}

func GetJSONByName[T any](h *e2e.Harness, resourceKind, name string) (T, error) {
	var zero T

	out, err := h.CLI(get, fmt.Sprintf("%s/%s", resourceKind, name), "-o", jsonStr)
	if err != nil {
		return zero, err
	}
	var obj T
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		return zero, err
	}
	return obj, nil
}

func ApplyFromExampleFile(harness *e2e.Harness, fileName string) (string, error) {
	return harness.CLI(apply, "-f", util.GetTestExamplesYamlPath(fileName))
}

func FilterWithFieldValueCondition(harness *e2e.Harness, resourceKind string, fieldName string, operator FieldSelectorOperator, fieldValue string) (string, error) {
	return harness.CLI(get, resourceKind, fieldSelectorSwitch, fmt.Sprintf("%s%s%s", fieldName, operator, fieldValue), "-o", wide)
}

func FilterWithLabelSelector(harness *e2e.Harness, resourceKind string, selector string) (string, error) {
	return harness.CLI(get, resourceKind, labelSelectorSwitch, selector, "-o", wide)
}

func FilterWithCreationTimeDuringCurrentYear(harness *e2e.Harness, resourceKind string, fieldName string) (string, error) {
	now := time.Now()
	startOfYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	endOfYear := time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
	return harness.CLI(get, resourceKind, fieldSelectorSwitch,
		fmt.Sprintf("%s>=%s,%s<%s", fieldName, startOfYear.Format(time.RFC3339), fieldName, endOfYear.Format(time.RFC3339)), "-o", wide)
}

func SomeRowsAreListedInResponse(response string, err error, expectedRows int) error {
	if err != nil {
		return err
	}
	if expectedRows == 0 && response == "" {
		return nil
	}
	if response == "" {
		return fmt.Errorf("no output to verify")
	}
	actualRows := len(strings.Split(response, "\n")) - 2 // exclude the header + closing newline
	if actualRows != expectedRows {
		return fmt.Errorf("expected '%d' rows in output, but got '%d'", expectedRows, actualRows)
	}
	return nil
}
