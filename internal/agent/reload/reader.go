package reload

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"

	"github.com/flightctl/flightctl/internal/agent/config"
	"sigs.k8s.io/yaml"
)

type reader struct {
	baseConfigFile string
	configDir      string
}

func newReader(baseConfigFile, configDir string) *reader {
	return &reader{
		baseConfigFile: baseConfigFile,
		configDir:      configDir,
	}
}

func (r *reader) readFile(filename string) (*config.Config, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var ret config.Config
	if err = yaml.Unmarshal(b, &ret); err != nil {
		return nil, err
	}
	return &ret, nil
}

func overrideIfNotEmpty[T any](dst *T, src T) {
	var empty T
	if !reflect.DeepEqual(empty, src) {
		*dst = src
	}
}

func (r *reader) overrideConfigs(base, overrides *config.Config) (*config.Config, error) {
	overrideIfNotEmpty(&base.LogLevel, overrides.LogLevel)
	overrideIfNotEmpty(&base.LogPrefix, overrides.LogPrefix)

	// TODO add all config values
	return base, nil
}

func (r *reader) readConfig() (*config.Config, error) {
	cfg, err := r.readFile(r.baseConfigFile)
	if err != nil {
		return nil, err
	}
	confSubdir := filepath.Join(r.configDir, "conf.d")
	entries, err := os.ReadDir(confSubdir)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	re := regexp.MustCompile(`^.*[.]ya?ml$`)
	for _, entry := range entries {
		if !re.MatchString(entry.Name()) {
			continue
		}
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(confSubdir, entry.Name())
		overrideConf, err := r.readFile(path)
		if err != nil {
			return nil, err
		}
		if cfg, err = r.overrideConfigs(cfg, overrideConf); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}
