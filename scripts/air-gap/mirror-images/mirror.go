package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/pkg/executer"
)

// ImagePair holds a resolved source→destination image reference pair.
// Source includes the full registry host and tag; Dest uses the caller-supplied
// destination registry with the same path and tag.
type ImagePair struct {
	Source string // e.g. "quay.io/flightctl/flightctl-api-el9:latest"
	Dest   string // e.g. "localhost:5000/flightctl/flightctl-api-el9:latest"
}

// normalizeDockerImage expands Docker Hub official image names to their
// canonical form by inserting the implicit "library/" namespace.
//
// Docker Hub stores official images (redis, nginx, postgres, etc.) under the
// "library" organization.  Podman and other runtimes resolve "docker.io/redis"
// as "docker.io/library/redis" at pull time, so a mirror that stores the image
// at registry/redis:tag will not be found.  This function makes the namespace
// explicit so both the skopeo source and destination paths are canonical.
//
// Only single-component docker.io paths are affected; multi-component paths
// (docker.io/grafana/grafana) and all other registries are returned unchanged.
func normalizeDockerImage(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 && parts[0] == "docker.io" && !strings.Contains(parts[1], "/") {
		return "docker.io/library/" + parts[1]
	}
	return image
}

// ImageToDest strips the source registry hostname from image and returns the
// full destination reference by prepending destRegistry.
//
// Example:
//
//	image = "quay.io/flightctl/flightctl-api-el9"
//	tag   = "latest"
//	dest  = "localhost:5000/flightctl/flightctl-api-el9:latest"
//
// The hostname is the first "/"-delimited component; everything after it
// (org/name) is preserved so the namespace structure is maintained on the
// destination registry.  Docker Hub official images are stored under library/
// so "docker.io/redis" → "registry/library/redis".
func ImageToDest(image, tag string) string {
	// SplitN with n=2 gives ["quay.io", "flightctl/flightctl-api-el9"]
	// or ["docker.io", "library/redis"] after normalization.
	parts := strings.SplitN(image, "/", 2)
	if len(parts) < 2 {
		// No "/" in image — treat the whole string as the path component.
		return destRegistry + "/" + image + ":" + tag
	}
	return destRegistry + "/" + parts[1] + ":" + tag
}

// Dedup removes ImagePairs with duplicate Source values, preserving the first
// occurrence order.  This handles images that appear in both helm-chart-opts.yaml
// and an observability images.yaml with identical source:tag pairs.
func Dedup(pairs []ImagePair) []ImagePair {
	seen := make(map[string]struct{}, len(pairs))
	out := make([]ImagePair, 0, len(pairs))

	for _, p := range pairs {
		if _, ok := seen[p.Source]; ok {
			continue // duplicate — skip
		}
		seen[p.Source] = struct{}{}
		out = append(out, p)
	}

	return out
}

// GenerateCommands prints one skopeo copy command per ImagePair to stdout and,
// when execute is true, also runs the command immediately.
//
// stdout carries only the skopeo commands (pipe-safe); all progress logs go to
// stderr via logInfo/logWarn/logError so they do not pollute captured output.
//
// When insecure is true, --dest-tls-verify=false is added to every command so
// skopeo can push to HTTP (non-TLS) destination registries.
//
// All images are attempted even when one fails so the operator gets a complete
// picture of what succeeded and what did not.  If any copy fails, an error is
// returned after the loop so callers (and CI) receive a non-zero exit code.
func GenerateCommands(ctx context.Context, pairs []ImagePair, execute, insecure bool, exec executer.Executer) error {
	logInfo("Generating skopeo copy commands...")
	logInfo("Total unique images to mirror: %d", len(pairs))

	var failed []string

	for _, p := range pairs {
		// Always print the command — this is the dry-run output and lets users
		// capture, review, or pipe it to bash without re-running the tool.
		var cmd string
		if insecure {
			cmd = fmt.Sprintf("skopeo copy --all --dest-tls-verify=false docker://%s docker://%s", p.Source, p.Dest)
		} else {
			cmd = fmt.Sprintf("skopeo copy --all docker://%s docker://%s", p.Source, p.Dest)
		}
		fmt.Println(cmd)

		if !execute {
			continue
		}

		// Execute the copy.  We pass individual arguments rather than running
		// through a shell so there is no injection risk from registry URLs.
		logInfo("Executing: %s", cmd)
		args := []string{"copy", "--all"}
		if insecure {
			args = append(args, "--dest-tls-verify=false")
		}
		args = append(args, "docker://"+p.Source, "docker://"+p.Dest)
		_, stderr, exitCode := exec.ExecuteWithContext(ctx, "skopeo", args...)
		if exitCode != 0 {
			logError("skopeo copy failed for %s (exit %d): %s — continuing", p.Source, exitCode, strings.TrimSpace(stderr))
			failed = append(failed, p.Source)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("%d image(s) failed to copy: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// logInfo writes an [INFO] prefixed message to stderr.
// Keeping all logs on stderr ensures stdout carries only skopeo commands.
func logInfo(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[INFO]  "+format+"\n", args...)
}

// logWarn writes a [WARN] prefixed message to stderr.
func logWarn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[WARN]  "+format+"\n", args...)
}

// logError writes an [ERROR] prefixed message to stderr.
func logError(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[ERROR] "+format+"\n", args...)
}
