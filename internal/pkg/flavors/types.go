package flavors

import "time"

// FlavorConfig represents the complete configuration for a specific flavor
type FlavorConfig struct {
	Name         string                 `yaml:"name,omitempty"`
	Description  string                 `yaml:"description,omitempty"`
	Home         string                 `yaml:"home,omitempty"`
	Icon         string                 `yaml:"icon,omitempty"`
	Annotations  map[string]string      `yaml:"annotations,omitempty"`
	BuildImages  BuildImagesConfig      `yaml:"buildImages,omitempty"`
	Images       map[string]ImageConfig `yaml:"images,omitempty"`
	AgentImages  AgentImagesConfig      `yaml:"agentImages,omitempty"`
	Timeouts     TimeoutsConfig         `yaml:"timeouts,omitempty"`
}

// FlavorConfigRaw represents the raw configuration as stored in YAML,
// including inheritance information
type FlavorConfigRaw struct {
	FlavorConfig `yaml:",inline"`
	Inherit      string `yaml:"_inherit,omitempty"`
}

// BuildImagesConfig represents build-time image configuration
type BuildImagesConfig struct {
	GoToolset   string         `yaml:"goToolset,omitempty"`
	UbiMinimal  string         `yaml:"ubiMinimal,omitempty"`
	Base        BaseImageConfig `yaml:"base,omitempty"`
}

// BaseImageConfig represents base image configuration
type BaseImageConfig struct {
	Image        string          `yaml:"image,omitempty"`
	Tag          string          `yaml:"tag,omitempty"`
	MinimalImage ImageNameTag    `yaml:"minimalImage,omitempty"`
}

// ImageConfig represents an image with optional tag
type ImageConfig struct {
	Image string `yaml:"image,omitempty"`
	Tag   string `yaml:"tag,omitempty"`
}

// ImageNameTag represents an image name and tag pair
type ImageNameTag struct {
	Image string `yaml:"image,omitempty"`
	Tag   string `yaml:"tag,omitempty"`
}

// AgentImagesConfig represents agent image configuration
type AgentImagesConfig struct {
	OsId            string `yaml:"osId,omitempty"`
	DeviceBaseImage string `yaml:"deviceBaseImage,omitempty"`
	EnableCrb       bool   `yaml:"enableCrb,omitempty"`
	EpelNext        bool   `yaml:"epelNext,omitempty"`
}

// TimeoutsConfig represents various timeout configurations
type TimeoutsConfig struct {
	DB        time.Duration `yaml:"db,omitempty"`
	KV        time.Duration `yaml:"kv,omitempty"`
	Migration time.Duration `yaml:"migration,omitempty"`
}

// FlavorsMap represents the complete collection of flavors
type FlavorsMap map[string]*FlavorConfigRaw