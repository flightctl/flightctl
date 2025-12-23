package util

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	ImageRepo = "quay.io/flightctl"
)

type ImageReference struct {
	Repo string
	Name string
	Tag  string
}

// Namespaced image names to avoid package-level collisions
type imageNames struct {
	Device   string
	SleepApp string
}

var ImageNames = imageNames{
	Device:   "flightctl-device",
	SleepApp: "sleep-app",
}

type versionTags struct {
	V1   string
	V2   string
	V3   string
	V4   string
	V5   string
	V6   string
	V7   string
	V8   string
	V9   string
	V10  string
	Base string
}

var DeviceTags = versionTags{
	V1:   "v1",
	V2:   "v2",
	V3:   "v3",
	V4:   "v4",
	V5:   "v5",
	V6:   "v6",
	V7:   "v7",
	V8:   "v8",
	V9:   "v9",
	V10:  "v10",
	Base: "base",
}

type sleepAppTags struct {
	V1 string
	V2 string
	V3 string
}

var SleepAppTags = sleepAppTags{
	V1: "v1",
	V2: "v2",
	V3: "v3",
}
var imageRefRe = regexp.MustCompile(
	// groups:
	// 1 = repo (everything before last "/")
	// 2 = name (last path segment)
	// 3 = tag  (optional)
	`^(.+)/([^\s/:]+)(?::([^\s/]+))?$`,
)

func NewImageReferenceFromString(imageRef string) (ImageReference, error) {
	s := strings.TrimSpace(imageRef)
	if s == "" {
		return ImageReference{}, fmt.Errorf("image reference is empty")
	}

	if strings.HasSuffix(s, ":") {
		return ImageReference{}, fmt.Errorf("image tag is empty in: %q", s)
	}

	m := imageRefRe.FindStringSubmatch(s)
	if m == nil {
		return ImageReference{}, fmt.Errorf("invalid image reference: %q", s)
	}

	repo := strings.TrimSpace(m[1])
	name := strings.TrimSpace(m[2])
	tag := ""
	if len(m) >= 4 {
		tag = strings.TrimSpace(m[3])
	}
	if repo == "" {
		return ImageReference{}, fmt.Errorf("image repo is empty in: %q", s)
	}
	if name == "" {
		return ImageReference{}, fmt.Errorf("image name is empty in: %q", s)
	}

	return NewImageReference(repo, name, tag), nil
}

func NewImageReference(repo, name, tag string) ImageReference {
	return ImageReference{
		Repo: repo,
		Name: name,
		Tag:  tag,
	}
}

func NewDeviceImageReference(tag string) ImageReference {
	return NewImageReference(ImageRepo, ImageNames.Device, tag)
}

func NewSleepAppImageReference(tag string) ImageReference {
	return NewImageReference(ImageRepo, ImageNames.SleepApp, tag)
}

func (r ImageReference) String() string {
	// If tag is empty, return without ":"
	if r.Tag == "" {
		return fmt.Sprintf("%s/%s", r.Repo, r.Name)
	}
	return fmt.Sprintf("%s/%s:%s", r.Repo, r.Name, r.Tag)
}

func (r ImageReference) WithTag(newTag string) ImageReference {
	return ImageReference{
		Repo: r.Repo,
		Name: r.Name,
		Tag:  newTag,
	}
}
