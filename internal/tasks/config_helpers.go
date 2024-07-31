package tasks

import (
	"fmt"
	"regexp"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

var (
	paramsRegex *regexp.Regexp = regexp.MustCompile(`(?P<full>{{\w*(?P<param>.*?)\w*}})`)
	labelRegex  *regexp.Regexp = regexp.MustCompile(`device.metadata.labels\[(?P<key>.*)\]`)
	nameRegex   *regexp.Regexp = regexp.MustCompile(`device.metadata.name`)
)

func ContainsParameter(b []byte) bool {
	return paramsRegex.Match(b)
}

func ReplaceParameters(b []byte, objectMeta api.ObjectMeta) ([]byte, error) {
	replacements := map[string]string{}
	paramsToMatches := map[string]string{}

	matches := paramsRegex.FindAllStringSubmatch(string(b), -1)
	for _, match := range matches {
		paramsToMatches[match[0]] = match[1]
	}

	for param, match := range paramsToMatches {
		switch {
		case nameRegex.MatchString(param):
			if objectMeta.Name == nil {
				return []byte(""), fmt.Errorf("parameter referenced name, but no name found")
			}
			replacements[match] = *objectMeta.Name
		case labelRegex.MatchString(param):
			key, err := findKeyinLabelParam(param)
			if err != nil {
				return []byte(""), err
			}
			if objectMeta.Labels == nil {
				return []byte(""), fmt.Errorf("no label found with key %s", key)
			}
			val, ok := (*objectMeta.Labels)[key]
			if !ok {
				return []byte(""), fmt.Errorf("no label found with key %s", key)
			}
			replacements[match] = val
		default:
			return []byte(""), fmt.Errorf("found unknown parameter: %s", param)
		}
	}

	outputStr := string(b)
	for old, new := range replacements {
		outputStr = strings.ReplaceAll(outputStr, old, new)
	}

	return []byte(outputStr), nil
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
