package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/scripts/pkg/e2etestutils"
	"github.com/stretchr/testify/require"
)

// Convenience aliases so test code stays readable.
type junitTestCase = e2etestutils.JUnitTestCase

var (
	junitSpecName        = e2etestutils.JUnitSpecName
	parseTimingsFromFile = e2etestutils.ParseTimingsFromFile
)

func TestRepoFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		envValue    string
		expectOwner string
		expectRepo  string
		expectErr   bool
	}{
		{
			name:        "When GITHUB_REPOSITORY is valid it should return owner and repo",
			envValue:    "flightctl/flightctl",
			expectOwner: "flightctl",
			expectRepo:  "flightctl",
		},
		{
			name:        "When GITHUB_REPOSITORY has a deep path it should split on the first slash only",
			envValue:    "myorg/my-repo",
			expectOwner: "myorg",
			expectRepo:  "my-repo",
		},
		{
			name:      "When GITHUB_REPOSITORY is empty it should return an error",
			envValue:  "",
			expectErr: true,
		},
		{
			name:      "When GITHUB_REPOSITORY has no slash it should return an error",
			envValue:  "noslash",
			expectErr: true,
		},
		{
			name:      "When GITHUB_REPOSITORY is just a slash it should return an error",
			envValue:  "/",
			expectErr: true,
		},
		{
			name:      "When GITHUB_REPOSITORY has an empty owner it should return an error",
			envValue:  "/repo",
			expectErr: true,
		},
		{
			name:      "When GITHUB_REPOSITORY has an empty repo it should return an error",
			envValue:  "owner/",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY", tt.envValue)

			owner, repo, err := repoFromEnv()
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectOwner, owner)
			require.Equal(t, tt.expectRepo, repo)
		})
	}
}

// makeJUnitReport builds a minimal JUnit XML file for testing.
func makeJUnitReport(t *testing.T, dir string, cases []junitTestCase) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString("<testsuites>\n<testsuite>\n")
	for _, tc := range cases {
		switch {
		case tc.Name == "":
			sb.WriteString(fmt.Sprintf(`  <testcase time="%.6f">`, tc.Time))
		case tc.ClassName != "":
			sb.WriteString(fmt.Sprintf(`  <testcase name=%q classname=%q time="%.6f">`, tc.Name, tc.ClassName, tc.Time))
		default:
			sb.WriteString(fmt.Sprintf(`  <testcase name=%q time="%.6f">`, tc.Name, tc.Time))
		}
		if len(tc.Skipped) > 0 {
			sb.WriteString("<skipped/>")
		}
		sb.WriteString("</testcase>\n")
	}
	sb.WriteString("</testsuite>\n</testsuites>\n")
	path := filepath.Join(dir, "junit_e2e_test.xml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o644))
	return path
}

func TestJunitSpecName(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "When name has prefix and label suffix it should return plain spec text",
			input:  "[It] Suite Context should do something [87884, sanity, agent]",
			expect: "Suite Context should do something",
		},
		{
			name:   "When name has prefix but no label suffix it should return spec text without prefix",
			input:  "[It] Suite Context should do something",
			expect: "Suite Context should do something",
		},
		{
			name:   "When name is BeforeSuite it should return empty string",
			input:  "[BeforeSuite]",
			expect: "",
		},
		{
			name:   "When name has no It prefix it should return empty string",
			input:  "Suite Context should do something",
			expect: "",
		},
		{
			name:   "When name is empty it should return empty string",
			input:  "",
			expect: "",
		},
		{
			name:   "When spec text itself contains brackets it should only strip the trailing label group",
			input:  "[It] Suite [tag] spec text [87884, sanity]",
			expect: "Suite [tag] spec text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, junitSpecName(tt.input))
		})
	}
}

func TestParseTimingsFromFile(t *testing.T) {
	tests := []struct {
		name         string
		cases        []junitTestCase
		expectKeys   []string
		expectAbsent []string
		expectErr    bool
	}{
		{
			name: "When testcase is a passing It spec it should store it with stripped key",
			cases: []junitTestCase{
				{Name: "[It] Suite Passed Spec [sanity]", Time: 45.0},
			},
			expectKeys: []string{"Suite Passed Spec"},
		},
		{
			name: "When testcase has a skipped element it should exclude the spec",
			cases: []junitTestCase{
				{Name: "[It] Suite Skipped Spec [sanity]", Time: 5.0, Skipped: []struct{}{{}}},
			},
			expectAbsent: []string{"Suite Skipped Spec"},
		},
		{
			name: "When testcase is BeforeSuite with classname it should store it as suite overhead",
			cases: []junitTestCase{
				{Name: "[BeforeSuite]", ClassName: "Agent E2E Suite", Time: 225.0},
			},
			expectKeys:   []string{"__suite__:Agent E2E Suite"},
			expectAbsent: []string{"[BeforeSuite]", ""},
		},
		{
			name: "When testcase is BeforeSuite without classname it should be excluded",
			cases: []junitTestCase{
				{Name: "[BeforeSuite]", Time: 10.0},
			},
			expectAbsent: []string{"[BeforeSuite]", "", "__suite__:"},
		},
		{
			name: "When testcase time is zero it should exclude the spec",
			cases: []junitTestCase{
				{Name: "[It] Zero Time Spec [sanity]", Time: 0},
			},
			expectAbsent: []string{"Zero Time Spec"},
		},
		{
			name: "When testcase time is negative it should exclude the spec",
			cases: []junitTestCase{
				{Name: "[It] Negative Spec [sanity]", Time: -1.0},
			},
			expectAbsent: []string{"Negative Spec"},
		},
		{
			name: "When testcase name is empty it should exclude the spec",
			cases: []junitTestCase{
				{Time: 10.0},
			},
			expectAbsent: []string{""},
		},
		{
			name: "When multiple specs appear it should include It specs and suite overhead, exclude skipped",
			cases: []junitTestCase{
				{Name: "[It] Suite Good A [sanity]", Time: 10.0},
				{Name: "[It] Suite Good B [sanity]", Time: 20.0},
				{Name: "[It] Suite Bad Skip [sanity]", Time: 5.0, Skipped: []struct{}{{}}},
				{Name: "[BeforeSuite]", ClassName: "My Suite", Time: 3.0},
			},
			expectKeys:   []string{"Suite Good A", "Suite Good B", "__suite__:My Suite"},
			expectAbsent: []string{"Suite Bad Skip", "[BeforeSuite]"},
		},
		{
			name: "When the same spec appears multiple times it should collect all observations",
			cases: []junitTestCase{
				{Name: "[It] Suite Repeated [sanity]", Time: 10.0},
				{Name: "[It] Suite Repeated [sanity]", Time: 20.0},
			},
			expectKeys: []string{"Suite Repeated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := makeJUnitReport(t, t.TempDir(), tt.cases)

			result, err := parseTimingsFromFile(path)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			for _, key := range tt.expectKeys {
				require.Contains(t, result, key, "expected spec %q to be present", key)
				require.NotEmpty(t, result[key], "expected observations for spec %q", key)
			}
			for _, key := range tt.expectAbsent {
				require.NotContains(t, result, key, "expected spec %q to be absent", key)
			}
		})
	}

	t.Run("When testcase time is 45s it should store the duration in seconds directly", func(t *testing.T) {
		path := makeJUnitReport(t, t.TempDir(), []junitTestCase{
			{Name: "[It] Suite Timed Spec [sanity]", Time: 45.0},
		})
		result, err := parseTimingsFromFile(path)
		require.NoError(t, err)
		require.InDelta(t, 45.0, result["Suite Timed Spec"][0], 1e-9)
	})

	t.Run("When the same spec appears twice it should accumulate both observations", func(t *testing.T) {
		path := makeJUnitReport(t, t.TempDir(), []junitTestCase{
			{Name: "[It] Suite Multi [sanity]", Time: 10.0},
			{Name: "[It] Suite Multi [sanity]", Time: 20.0},
		})
		result, err := parseTimingsFromFile(path)
		require.NoError(t, err)
		require.Len(t, result["Suite Multi"], 2)
	})

	t.Run("When the file contains invalid XML it should return an error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.xml")
		require.NoError(t, os.WriteFile(path, []byte("not xml <<"), 0o644))
		_, err := parseTimingsFromFile(path)
		require.Error(t, err)
	})
}

func TestLoadExistingCache(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		writeFile bool
		expectMap map[string]specTiming
	}{
		{
			name:      "When cache file does not exist it should return empty map with no error",
			writeFile: false,
			expectMap: map[string]specTiming{},
		},
		{
			name:      "When cache file is valid JSON it should return the correct map",
			writeFile: true,
			content:   `{"Spec A": {"avg": 45.3, "stddev": 2.1}, "Spec B": {"avg": 120.0, "stddev": 0}}`,
			expectMap: map[string]specTiming{"Spec A": {Avg: 45.3, StdDev: 2.1}, "Spec B": {Avg: 120.0, StdDev: 0}},
		},
		{
			name:      "When cache file contains invalid JSON it should return empty map gracefully",
			writeFile: true,
			content:   "not json",
			expectMap: map[string]specTiming{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "cache.json")
			if tt.writeFile {
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o644))
			}

			got, err := loadExistingCache(path)
			require.NoError(t, err)
			require.Equal(t, tt.expectMap, got)
		})
	}
}

func TestWriteCache(t *testing.T) {
	t.Run("When writing a cache it should produce valid parseable JSON", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "timings.json")
		input := map[string]specTiming{
			"Spec A": {Avg: 45.3, StdDev: 2.1},
			"Spec B": {Avg: 120.0, StdDev: 0},
		}

		require.NoError(t, writeCache(path, input))

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		var got map[string]specTiming
		require.NoError(t, json.Unmarshal(data, &got))
		require.Equal(t, len(input), len(got))
	})

	t.Run("When writing a cache it should sort keys alphabetically", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "timings.json")
		input := map[string]specTiming{
			"Zebra":  {Avg: 1.0},
			"Alpha":  {Avg: 2.0},
			"Middle": {Avg: 3.0},
		}

		require.NoError(t, writeCache(path, input))

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		// Verify the raw bytes contain keys in order.
		content := string(data)
		alphaIdx := indexOf(content, `"Alpha"`)
		middleIdx := indexOf(content, `"Middle"`)
		zebraIdx := indexOf(content, `"Zebra"`)
		require.Less(t, alphaIdx, middleIdx, "Alpha should appear before Middle")
		require.Less(t, middleIdx, zebraIdx, "Middle should appear before Zebra")
	})

	t.Run("When writing a cache it should round avg and stddev to millisecond precision", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "timings.json")
		input := map[string]specTiming{
			"Precise Spec": {Avg: 45.123456789, StdDev: 12.987654321},
		}

		require.NoError(t, writeCache(path, input))

		var got map[string]specTiming
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &got))

		require.InDelta(t, math.Round(45.123456789*1000)/1000, got["Precise Spec"].Avg, 1e-9)
		require.InDelta(t, math.Round(12.987654321*1000)/1000, got["Precise Spec"].StdDev, 1e-9)
	})

	t.Run("When the output directory does not exist it should return an error", func(t *testing.T) {
		err := writeCache(filepath.Join(t.TempDir(), "nonexistent", "out.json"), map[string]specTiming{})
		require.Error(t, err)
	})
}

// indexOf returns the byte index of substr in s, or -1 if not found.
func indexOf(s, substr string) int {
	for i := range s {
		if i+len(substr) <= len(s) && s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
