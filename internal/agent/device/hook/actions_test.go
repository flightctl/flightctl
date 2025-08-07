package hook

import (
	"os"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

func TestSplitCommandAndArgs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "simple command",
			input:    "echo hello world",
			wantCmd:  "echo",
			wantArgs: []string{"hello", "world"},
		},
		{
			name:     "command with -c and quoted script",
			input:    `/usr/bin/bash -c "echo hello world"`,
			wantCmd:  "/usr/bin/bash",
			wantArgs: []string{"-c", "echo hello world"},
		},
		{
			name:     "quoted with inner env var",
			input:    `/usr/bin/bash -c "until [ -f $KUBECONFIG ]; do sleep 1; done"`,
			wantCmd:  "/usr/bin/bash",
			wantArgs: []string{"-c", "until [ -f $KUBECONFIG ]; do sleep 1; done"},
		},
		{
			name:     "quoted no inner var",
			input:    `/usr/bin/bash -c "until [ -f /path ]; do sleep 1; done"`,
			wantCmd:  "/usr/bin/bash",
			wantArgs: []string{"-c", "until [ -f /path ]; do sleep 1; done"},
		},

		{
			name:     "unmatched quote",
			input:    `/usr/bin/bash -c "unmatched`,
			wantCmd:  "/usr/bin/bash",
			wantArgs: []string{"-c", `"unmatched`},
		},
		{
			name:     "single-word command",
			input:    "uptime",
			wantCmd:  "uptime",
			wantArgs: []string{},
		},
		{
			name:     "empty input",
			input:    "",
			wantCmd:  "",
			wantArgs: []string{},
		},
		{
			name:     "tab delimited arguments",
			input:    "echo\tfoo\tbar",
			wantCmd:  "echo",
			wantArgs: []string{"foo", "bar"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			cmd, args := splitCommandAndArgs(tt.input)
			require.Equal(tt.wantCmd, cmd, "unexpected command")
			require.Equal(tt.wantArgs, args, "unexpected args")
		})
	}
}

func TestSplitWithQuotes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple split",
			input:    "/usr/bin/bash -c echo hello",
			expected: []string{"/usr/bin/bash", "-c", "echo", "hello"},
		},
		{
			name:     "quoted argument with spaces",
			input:    `/usr/bin/bash -c "echo hello world"`,
			expected: []string{"/usr/bin/bash", "-c", "echo hello world"},
		},
		{
			name:     "single quotes preserved",
			input:    `/usr/bin/bash -c 'until [ -f /path ]; do sleep 1; done'`,
			expected: []string{"/usr/bin/bash", "-c", "until [ -f /path ]; do sleep 1; done"},
		},
		{
			name:     "escaped internal quotes",
			input:    `/usr/bin/bash -c "until [ -f /path ]; do echo \"waiting\"; sleep 1; done"`,
			expected: []string{"/usr/bin/bash", "-c", "until [ -f /path ]; do echo \"waiting\"; sleep 1; done"},
		},
		{
			name:     "empty input",
			input:    "",
			expected: []string{},
		},
		{
			name:     "unmatched double quote",
			input:    `/usr/bin/bash -c "unterminated quote`,
			expected: []string{"/usr/bin/bash", "-c", `"unterminated quote`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			actual := splitWithQuotes(tt.input)
			require.Equal(tt.expected, actual, "input: %q", tt.input)
		})
	}
}

func TestCheckRunActionDependency(t *testing.T) {
	require := require.New(t)
	tempDir := t.TempDir()

	readWriter := fileio.NewReadWriter()
	readWriter.SetRootdir(tempDir)
	err := readWriter.WriteFile("executable.sh", []byte("#!/bin/bash\necho 'Hello'"), 0755)
	require.NoError(err)
	err = readWriter.WriteFile("non-executable.txt", []byte("Just some text"), 0644)
	require.NoError(err)
	err = readWriter.MkdirAll("subdir", 0755)
	require.NoError(err)

	ogPath := os.Getenv("PATH")
	newPath := tempDir
	require.NoError(os.Setenv("PATH", newPath))
	t.Cleanup(func() {
		_ = os.Setenv("PATH", ogPath)
	})

	tests := []struct {
		name    string
		action  v1alpha1.HookActionRun
		wantErr error
	}{
		{
			name:   "valid executable",
			action: v1alpha1.HookActionRun{Run: "executable.sh"},
		},
		{
			name:    "non-executable file",
			action:  v1alpha1.HookActionRun{Run: "non-executable.txt"},
			wantErr: exec.ErrNotFound,
		},
		{
			name:    "directory instead of file",
			action:  v1alpha1.HookActionRun{Run: "subdir"},
			wantErr: exec.ErrNotFound,
		},
		{
			name:    "invalid path",
			action:  v1alpha1.HookActionRun{Run: "/invalid/path/to/executable"},
			wantErr: errors.ErrNotExist,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkRunActionDependency(tt.action)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			} else {
				require.NoError(err)
			}
		})
	}
}

func TestReplaceTokensInrun(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		run      string
		tokens   map[CommandLineVarKey]string
		wantErr  error
		expected string
	}{
		{
			name:     "no tokens",
			run:      "foo bar baz",
			tokens:   map[CommandLineVarKey]string{},
			expected: "foo bar baz",
		},
		{
			name:     "single token",
			run:      "foo bar baz ${Path}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced"},
			expected: "foo bar baz replaced",
		},
		{
			name:     "multiple same tokens",
			run:      "foo bar baz ${ Path} ${Path}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "replaced"},
			expected: "foo bar baz replaced replaced",
		},
		{
			name:     "token not found",
			run:      "${DoesNotExist} foo bar baz",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced"},
			expected: "${DoesNotExist} foo bar baz",
		},
		{
			name:     "multiple different tokens odd spacing",
			run:      "${ Path} foo bar baz ${ Files  }",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "a b c"},
			expected: "replaced foo bar baz a b c",
		},
		{
			name:     "multiple different tokens even spacing",
			run:      "${ Path} foo bar baz ${ Files}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "a b c"},
			expected: "replaced foo bar baz a b c",
		},
		{
			name:     "multiple different tokens no spacing",
			run:      "${Path} foo bar baz ${Files}",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "a b c"},
			expected: "replaced foo bar baz a b c",
		},
		{
			name:     "invalid token syntax",
			run:      "${Path foo bar baz",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced"},
			expected: "${Path foo bar baz",
		},
		{
			name:     "empty token value",
			run:      "${Path} foo ${Files} bar",
			tokens:   map[CommandLineVarKey]string{PathKey: "", FilesKey: "files"},
			expected: " foo files bar",
		},
		{
			name:     "mixed syntax",
			run:      "${Path} foo {{Files}} bar",
			tokens:   map[CommandLineVarKey]string{PathKey: "replaced", FilesKey: "files"},
			expected: "replaced foo {{Files}} bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceTokens(tt.run, tt.tokens)
			require.Equal(tt.expected, got)
		})
	}
}

var testTokens = map[CommandLineVarKey]string{
	PathKey:  "replaced",
	FilesKey: "a b c",
}

var testString = "foo bar ${Path} baz ${ Files } something ${ DoesNotExist } end"

func BenchmarkReplaceTokensOptimized(b *testing.B) {
	for i := 0; i < b.N; i++ {
		replaceTokens(testString, testTokens)
	}
}
