package tasks

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	paramsRegex *regexp.Regexp = regexp.MustCompile(`(?P<full>{{\w*(?P<param>.*?)\w*}})`)
	labelRegex  *regexp.Regexp = regexp.MustCompile(`device.metadata.labels\[(?P<key>.*)\]`)
)

func ContainsParameter(b []byte) bool {
	return paramsRegex.Match(b)
}

func ReplaceParameters(b []byte, labels *map[string]string) ([]byte, error) {
	replacements := map[string]string{}
	paramsToMatches := map[string]string{}

	if labels == nil {
		return b, nil
	}

	matches := paramsRegex.FindAllStringSubmatch(string(b), -1)
	for _, match := range matches {
		paramsToMatches[match[0]] = match[1]
	}

	for param, match := range paramsToMatches {
		if !labelRegex.MatchString(param) {
			return []byte(""), fmt.Errorf("found unknown parameter: %s", param)
		}

		key, err := findKeyinLabelParam(param)
		if err != nil {
			return []byte(""), err
		}

		val, ok := (*labels)[key]
		if !ok {
			return []byte(""), fmt.Errorf("no label found with key %s", key)
		}

		replacements[match] = val
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
