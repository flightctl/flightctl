package validation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateGenericName(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"a",
		"a-good-name",
		strings.Repeat("a", 63),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateGenericName(&val, "good.name"))
	}

	badValues := []string{
		"",
		"-starts-with-dash",
		"with.dots",
		"with_underscores",
		"WITH-CAPITAL-LETTERS",
		strings.Repeat("long", 16),
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateGenericName(&val, "bad.name"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateOciImageReference(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"flightctl",
		"flightctl:latest",
		"flightctl:v0.0.1",
		"quay.io/flightctl",
		"quay.io/flightctl/flightctl",
		"quay.io/flightctl/flightctl:latest",
		"quay.io/flightctl/flightctl@sha256:0123456789abcdef0123456789abcdef",
		"quay.io/flightctl/flightctl:latest@sha256:0123456789abcdef0123456789abcdef",
		"weird__but_valid",
		strings.Repeat("a", 64),
		"image:" + strings.Repeat("a", 128),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateOciImageReference(&val, "good.image.ref"))
	}

	badValues := []string{
		"_underscore",
		"not.a//domain",
		"quay.io/flightctl/flightctl@sha256:0123456789abcdef0123456789abcde",
		"image:" + strings.Repeat("a", 129),
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateOciImageReference(&val, "bad.image.ref"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateGitRevision(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"main",
		"latest",
		"v1.0.0",
		"9fac431b7f4f319ead0195034064012e732bbb0c",
		"weird__but--valid..branch//name",
		strings.Repeat("a", 244),
	}
	for _, val := range goodValues {
		assert.Empty(ValidateGitRevision(&val, "good.image.ref"))
	}

	badValues := []string{
		"contains whitespace",
		"_startswithunderscore",
		strings.Repeat("a", 245),
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateGitRevision(&val, "bad.image.ref"), fmt.Sprintf("value: %q", val))
	}
}

func TestValidateSystemdUnitPattern(t *testing.T) {
	assert := assert.New(t)

	goodValues := []string{
		"foo.service",
		"service",
		"foo[0-9].service",
		"foo?.service",
		"foo\\.service",
	}
	for _, val := range goodValues {
		assert.Empty(ValidateSystemdName(&val, "good.unit"))
	}

	badValues := []string{
		"foo@@bar.service",
		"",
		"@bar.service",
		"foo;bar.service",
	}
	for _, val := range badValues {
		assert.NotEmpty(ValidateSystemdName(&val, "bad.unit"), fmt.Sprintf("value: %q", val))
	}
}
