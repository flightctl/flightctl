package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// UBITag represents a parsed UBI image tag like "9.7-1762965531".
type UBITag struct {
	Raw       string
	Major     int
	Minor     int
	Timestamp int
}

// ParseUBITag parses a UBI-style tag "MAJOR.MINOR-TIMESTAMP".
func ParseUBITag(tag string) (UBITag, error) {
	parts := strings.SplitN(tag, "-", 2)
	if len(parts) != 2 {
		return UBITag{}, fmt.Errorf("invalid UBI tag %q: expected MAJOR.MINOR-TIMESTAMP", tag)
	}

	verParts := strings.SplitN(parts[0], ".", 2)
	if len(verParts) != 2 {
		return UBITag{}, fmt.Errorf("invalid UBI tag %q: expected MAJOR.MINOR in version part", tag)
	}

	major, err := strconv.Atoi(verParts[0])
	if err != nil {
		return UBITag{}, fmt.Errorf("invalid UBI tag %q: major version: %w", tag, err)
	}

	minor, err := strconv.Atoi(verParts[1])
	if err != nil {
		return UBITag{}, fmt.Errorf("invalid UBI tag %q: minor version: %w", tag, err)
	}

	timestamp, err := strconv.Atoi(parts[1])
	if err != nil {
		return UBITag{}, fmt.Errorf("invalid UBI tag %q: timestamp: %w", tag, err)
	}

	return UBITag{
		Raw:       tag,
		Major:     major,
		Minor:     minor,
		Timestamp: timestamp,
	}, nil
}

// GoToolsetTag represents a parsed go-toolset tag like "1.26.7-1787774815".
type GoToolsetTag struct {
	Raw       string
	Major     int
	Minor     int
	Patch     int
	Timestamp int
}

// ParseGoToolsetTag parses a go-toolset tag "MAJOR.MINOR.PATCH-TIMESTAMP".
func ParseGoToolsetTag(tag string) (GoToolsetTag, error) {
	parts := strings.SplitN(tag, "-", 2)
	if len(parts) != 2 {
		return GoToolsetTag{}, fmt.Errorf("invalid go-toolset tag %q: expected MAJOR.MINOR.PATCH-TIMESTAMP", tag)
	}

	verParts := strings.Split(parts[0], ".")
	if len(verParts) != 3 {
		return GoToolsetTag{}, fmt.Errorf("invalid go-toolset tag %q: expected MAJOR.MINOR.PATCH", tag)
	}

	major, err := strconv.Atoi(verParts[0])
	if err != nil {
		return GoToolsetTag{}, fmt.Errorf("invalid go-toolset tag %q: major: %w", tag, err)
	}
	minor, err := strconv.Atoi(verParts[1])
	if err != nil {
		return GoToolsetTag{}, fmt.Errorf("invalid go-toolset tag %q: minor: %w", tag, err)
	}
	patch, err := strconv.Atoi(verParts[2])
	if err != nil {
		return GoToolsetTag{}, fmt.Errorf("invalid go-toolset tag %q: patch: %w", tag, err)
	}
	timestamp, err := strconv.Atoi(parts[1])
	if err != nil {
		return GoToolsetTag{}, fmt.Errorf("invalid go-toolset tag %q: timestamp: %w", tag, err)
	}

	return GoToolsetTag{
		Raw:       tag,
		Major:     major,
		Minor:     minor,
		Patch:     patch,
		Timestamp: timestamp,
	}, nil
}

// UBITagPattern returns a regex matching UBI tags for a given major version.
// Example: UBITagPattern("9") matches "9.7-1762965531".
func UBITagPattern(majorVersion string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(majorVersion) + `\.[0-9]+-[0-9]+$`)
}

// GoToolsetTagPattern returns a regex matching go-toolset tags for a Go minor version.
// Example: GoToolsetTagPattern("1.26") matches "1.26.7-1787774815".
func GoToolsetTagPattern(goMinor string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(goMinor) + `\.[0-9]+-[0-9]+$`)
}

// LatestMatchingTag finds the latest tag from a list that matches the given pattern.
// Tags are compared by their parsed version components (major.minor or
// major.minor.patch) first; the timestamp suffix is used only as a
// tiebreaker when versions are equal.
// Returns empty string if no tags match.
func LatestMatchingTag(tags []string, pattern *regexp.Regexp) string {
	var matching []string
	for _, t := range tags {
		if pattern.MatchString(t) {
			matching = append(matching, t)
		}
	}
	if len(matching) == 0 {
		return ""
	}

	sort.Slice(matching, func(i, j int) bool {
		return compareTagVersions(matching[i], matching[j]) < 0
	})

	return matching[len(matching)-1]
}

// compareTagVersions compares two tags by parsed version components first,
// then by timestamp as a tiebreaker. Returns negative if a < b, 0 if equal,
// positive if a > b.
func compareTagVersions(a, b string) int {
	// Try parsing as GoToolset tags first (MAJOR.MINOR.PATCH-TS),
	// then fall back to UBI tags (MAJOR.MINOR-TS).
	aGo, aGoErr := ParseGoToolsetTag(a)
	bGo, bGoErr := ParseGoToolsetTag(b)
	if aGoErr == nil && bGoErr == nil {
		if aGo.Major != bGo.Major {
			return aGo.Major - bGo.Major
		}
		if aGo.Minor != bGo.Minor {
			return aGo.Minor - bGo.Minor
		}
		if aGo.Patch != bGo.Patch {
			return aGo.Patch - bGo.Patch
		}
		return aGo.Timestamp - bGo.Timestamp
	}

	aUBI, aUBIErr := ParseUBITag(a)
	bUBI, bUBIErr := ParseUBITag(b)
	if aUBIErr == nil && bUBIErr == nil {
		if aUBI.Major != bUBI.Major {
			return aUBI.Major - bUBI.Major
		}
		if aUBI.Minor != bUBI.Minor {
			return aUBI.Minor - bUBI.Minor
		}
		return aUBI.Timestamp - bUBI.Timestamp
	}

	// Fallback: compare by timestamp only.
	return timestampFromTag(a) - timestampFromTag(b)
}

// timestampFromTag extracts the numeric timestamp suffix from a tag.
// For "9.7-1762965531" returns 1762965531.
// For "1.26.7-1787774815" returns 1787774815.
func timestampFromTag(tag string) int {
	idx := strings.LastIndex(tag, "-")
	if idx < 0 {
		return 0
	}
	ts, err := strconv.Atoi(tag[idx+1:])
	if err != nil {
		return 0
	}
	return ts
}

// ExtractGoMinorVersion reads a go.mod file and extracts the Go minor version
// from the "toolchain" directive. For "toolchain go1.26.7" it returns "1.26".
func ExtractGoMinorVersion(goModContent string) (string, error) {
	re := regexp.MustCompile(`(?m)^toolchain go([0-9]+\.[0-9]+)`)
	matches := re.FindStringSubmatch(goModContent)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not extract Go toolchain version from go.mod")
	}
	return matches[1], nil
}
