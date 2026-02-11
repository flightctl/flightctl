package service

import (
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	// OCI image name component format as per https://github.com/containers/image/blob/main/docker/reference/regexp.go
	// Repository name components: lowercase alphanumeric with dots, underscores, and hyphens
	// Cannot start or end with a separator
	ociImageNameComponentFmt string = `[a-z0-9]+(?:(?:(?:[._]|__|[-]*)[a-z0-9]+)+)?`
	// Full repository name: one or more components separated by forward slashes
	ociImageRepositoryNameFmt string = `(?:` + ociImageNameComponentFmt + `)(?:\/` + ociImageNameComponentFmt + `)*`
	// Maximum length for repository name (reasonable limit)
	ociImageRepositoryNameMaxLength int = 255

	// OCI image tag format as per https://github.com/containers/image/blob/main/docker/reference/regexp.go
	// Tag: must start with word character, then word characters, dots, or hyphens
	// Maximum length is 128 characters
	ociImageTagFmt       string = `[\w][\w.-]{0,127}`
	ociImageTagMaxLength int    = 128
)

var (
	ociImageRepositoryNameRegexp = regexp.MustCompile("^" + ociImageRepositoryNameFmt + "$")
	ociImageTagRegexp            = regexp.MustCompile("^" + ociImageTagFmt + "$")
)

// ValidateImageName validates an OCI image repository name according to RFC specifications.
// Repository names must:
// - Consist of lowercase alphanumeric characters
// - May contain dots, underscores, and hyphens as separators
// - Cannot start or end with a separator
// - Components are separated by forward slashes
func ValidateImageName(imageName *string, path string) []error {
	if imageName == nil || *imageName == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	var errs []error
	if len(*imageName) > ociImageRepositoryNameMaxLength {
		errs = append(errs, field.TooLong(fieldPathFor(path), imageName, ociImageRepositoryNameMaxLength))
	}
	if !ociImageRepositoryNameRegexp.MatchString(*imageName) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *imageName, "must match OCI repository name format: lowercase alphanumeric with dots, underscores, or hyphens, separated by forward slashes"))
	}
	return errs
}

// ValidateImageTag validates an OCI image tag according to RFC specifications.
// Tags must:
// - Start with a word character (letter, digit, or underscore)
// - May contain word characters, dots, and hyphens
// - Cannot start with a period or dash
// - Maximum length is 128 characters
func ValidateImageTag(imageTag *string, path string) []error {
	if imageTag == nil || *imageTag == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	var errs []error
	if len(*imageTag) > ociImageTagMaxLength {
		errs = append(errs, field.TooLong(fieldPathFor(path), imageTag, ociImageTagMaxLength))
	}
	if !ociImageTagRegexp.MatchString(*imageTag) {
		errs = append(errs, field.Invalid(fieldPathFor(path), *imageTag, "must match OCI tag format: start with alphanumeric or underscore, may contain alphanumeric, dots, hyphens, or underscores, max 128 characters"))
	}
	return errs
}

// fieldPathFor creates a field path from a string path
func fieldPathFor(path string) *field.Path {
	fields := strings.Split(path, ".")
	return field.NewPath(fields[0], fields[1:]...)
}

const (
	// Maximum length for username (reasonable limit for system usernames)
	usernameMaxLength int = 256

	// Maximum length for SSH public key (typical SSH keys are much shorter, but allow some margin)
	publicKeyMaxLength int = 8192

	// Username format: RHEL/Fedora useradd rules
	// - First char: letter, digit, underscore, or dot
	// - Subsequent chars: letters, digits, underscore, dot, or hyphen
	// - Optional trailing '$' (for Samba machine accounts)
	// - '@' is NOT allowed (rejected by useradd)
	usernameFmt string = `[A-Za-z0-9_.][A-Za-z0-9_.-]*\$?`
)

var (
	usernameRegexp = regexp.MustCompile("^" + usernameFmt + "$")
)

// ValidateUsername validates a username to prevent Containerfile injection attacks.
// Username must follow RHEL/Fedora useradd rules:
// - First char: letter, digit, underscore, or dot
// - Subsequent chars: letters, digits, underscore, dot, or hyphen
// - Optional trailing '$' (for Samba machine accounts)
// - '@' is NOT allowed (rejected by useradd)
// - Be reasonable length (max 256 characters)
// - Not have leading or trailing whitespace
func ValidateUsername(username *string, path string) []error {
	if username == nil || *username == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	// Check for leading/trailing whitespace
	trimmed := strings.TrimSpace(*username)
	if trimmed != *username {
		return []error{field.Invalid(fieldPathFor(path), *username, "cannot have leading or trailing whitespace")}
	}

	// Username should not be empty after trimming
	if trimmed == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	// Check length
	if len(trimmed) > usernameMaxLength {
		return []error{field.TooLong(fieldPathFor(path), username, usernameMaxLength)}
	}

	// Validate against whitelist pattern
	if !usernameRegexp.MatchString(trimmed) {
		return []error{field.Invalid(fieldPathFor(path), *username, "invalid characters; first char must be letter/digit/underscore/dot, subsequent chars allow hyphen, optional trailing '$', '@' is not allowed")}
	}

	return nil
}

// ValidatePublicKey validates an SSH public key to prevent Containerfile injection attacks.
// Public key must:
// - Be a valid SSH public key format (starts with key type like "ssh-rsa", "ssh-ed25519", etc.)
// - Not contain dangerous characters that could break out of context
// - Have reasonable length (max 8192 characters)
// - Follow SSH public key format: "key-type base64-data [comment]"
func ValidatePublicKey(publicKey *string, path string) []error {
	if publicKey == nil || *publicKey == "" {
		return []error{field.Required(fieldPathFor(path), "")}
	}

	var errs []error

	// Check length
	if len(*publicKey) > publicKeyMaxLength {
		errs = append(errs, field.TooLong(fieldPathFor(path), publicKey, publicKeyMaxLength))
	}

	// Check for dangerous characters that could be used for injection
	// Allow newlines only at the end (SSH keys often have trailing newline)
	trimmedKey := strings.TrimRight(*publicKey, "\n\r")

	// Check for embedded newlines/carriage returns (prevent Containerfile injection)
	if strings.ContainsAny(trimmedKey, "\n\r") {
		errs = append(errs, field.Invalid(fieldPathFor(path), "***", "embedded newlines are forbidden"))
		return errs
	}

	// Check for dangerous characters in the actual key content (excluding trailing newlines)
	if strings.ContainsAny(trimmedKey, ";|&`(){}[]<>\"'\\\t$") {
		errs = append(errs, field.Invalid(fieldPathFor(path), "***", "contains unsafe characters that could be used for Containerfile injection"))
	}

	// Validate SSH public key format
	// SSH public keys typically start with: ssh-rsa, ssh-ed25519, ecdsa-sha2-nistp256, etc.
	// Format: "key-type base64-data [optional-comment]"
	parts := strings.Fields(trimmedKey)
	if len(parts) < 2 {
		errs = append(errs, field.Invalid(fieldPathFor(path), "***", "invalid SSH public key format: must contain key type and key data"))
		return errs
	}

	// Validate key type (first part)
	keyType := parts[0]
	validKeyTypes := []string{
		"ssh-rsa",
		"ssh-ed25519",
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
		"ssh-dss", // DSA (deprecated but still seen)
	}
	validType := false
	for _, valid := range validKeyTypes {
		if keyType == valid {
			validType = true
			break
		}
	}
	if !validType {
		errs = append(errs, field.Invalid(fieldPathFor(path), "***", "invalid SSH public key type: must be one of ssh-rsa, ssh-ed25519, ecdsa-sha2-nistp256, ecdsa-sha2-nistp384, ecdsa-sha2-nistp521"))
	}

	// Validate base64 data (second part)
	base64Data := parts[1]
	// Base64 characters: A-Z, a-z, 0-9, +, /, = (for padding)
	base64Regex := regexp.MustCompile(`^[A-Za-z0-9+/]+=*$`)
	if !base64Regex.MatchString(base64Data) {
		errs = append(errs, field.Invalid(fieldPathFor(path), "***", "invalid SSH public key data: must be valid base64"))
	}

	// Check minimum length for base64 data (should be substantial for a real key)
	if len(base64Data) < 50 {
		errs = append(errs, field.Invalid(fieldPathFor(path), "***", "SSH public key data appears too short to be valid"))
	}

	return errs
}
