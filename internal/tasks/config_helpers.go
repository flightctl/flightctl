package tasks

import (
	"fmt"
	"regexp"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

var (
	paramsRegex *regexp.Regexp = regexp.MustCompile(`(?P<full>{{\w*(?P<param>.*?)\w*}})`)
	labelRegex  *regexp.Regexp = regexp.MustCompile(`^(?P<full>{{\s*(?:(?P<label>device\.metadata\.labels\[(?P<key>.*)\]))\s*}})$`)
	nameRegex   *regexp.Regexp = regexp.MustCompile(`^(?P<full>{{\s*(?:(?P<name>device\.metadata\.name))\s*}})$`)
)

func ContainsParameter(b []byte) bool {
	return paramsRegex.Match(b)
}

func ValidateParameterFormat(b []byte) error {
	matches := paramsRegex.FindAllStringSubmatch(string(b), -1)
	for _, match := range matches {
		param := match[0]
		if !labelRegex.MatchString(param) && !nameRegex.MatchString(param) {
			return fmt.Errorf("invalid parameter: %s", param)
		}
	}
	return nil
}

func ReplaceParameters(b []byte, objectMeta api.DeviceMetadata) ([]byte, []string) {
	replacements := map[string]string{}
	paramsToMatches := map[string]string{}

	matches := paramsRegex.FindAllStringSubmatch(string(b), -1)
	for _, match := range matches {
		paramsToMatches[match[0]] = match[1]
	}

	warnings := []string{}

	for param, match := range paramsToMatches {
		switch {
		case nameRegex.MatchString(param):
			if objectMeta.Name == nil {
				warnings = append(warnings, "parameter referenced name, but no name found")
				continue
			}
			replacements[match] = *objectMeta.Name
		case labelRegex.MatchString(param):
			key, err := findKeyinLabelParam(param)
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			if objectMeta.Labels == nil {
				warnings = append(warnings, fmt.Sprintf("no label found with key %s", key))
				continue
			}
			val, ok := (*objectMeta.Labels)[key]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("no label found with key %s", key))
				continue
			}
			replacements[match] = val
		default:
			warnings = append(warnings, fmt.Sprintf("found unknown parameter: %s", param))
		}
	}

	outputStr := string(b)
	for old, new := range replacements {
		outputStr = strings.ReplaceAll(outputStr, old, new)
	}

	return []byte(outputStr), warnings
}

func findKeyinLabelParam(param string) (string, error) {
	matches := labelRegex.FindStringSubmatch(param)
	for i, name := range labelRegex.SubexpNames() {
		if name == "key" {
			return matches[i], nil
		}
	}
	return "", fmt.Errorf("could not find label key in param %s", param)
}
