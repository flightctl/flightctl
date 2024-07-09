package ignition

import (
	"encoding/json"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/samber/lo"
	"github.com/vincent-petithory/dataurl"
)

const initialIgnition = `{"ignition": {"version": "3.4.0"}}`

type Wrapper interface {
	SetFile(filePath string, contents []byte, mode int)
	AsJson() ([]byte, error)
	AsMap() (map[string]interface{}, error)
	Merge(parent config_latest_types.Config) config_latest_types.Config
}

type wrapper struct {
	config config_latest_types.Config
}

func NewWrapper() (Wrapper, error) {
	cfg, _, err := config_latest.ParseCompatibleVersion([]byte(initialIgnition))
	if err != nil {
		return nil, err
	}
	return &wrapper{
		config: cfg,
	}, nil
}

func (w *wrapper) SetFile(filePath string, contents []byte, mode int) {
	file := config_latest_types.File{
		Node: config_latest_types.Node{
			Path:      filePath,
			Overwrite: lo.ToPtr(true),
			Group:     config_latest_types.NodeGroup{},
			User:      config_latest_types.NodeUser{Name: lo.ToPtr("root")},
		},
		FileEmbedded1: config_latest_types.FileEmbedded1{
			Contents: config_latest_types.Resource{
				Source: lo.ToPtr(dataurl.New(contents, "text/plain").String()),
			},
			Mode: &mode,
		},
	}
	w.config.Storage.Files = append(w.config.Storage.Files, file)
}

func (w *wrapper) AsJson() ([]byte, error) {
	b, err := json.Marshal(&w.config)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (w *wrapper) AsMap() (map[string]interface{}, error) {
	b, err := json.Marshal(&w.config)
	if err != nil {
		return nil, err
	}
	ret := make(map[string]interface{})
	if err = json.Unmarshal(b, &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func (w *wrapper) Merge(parent config_latest_types.Config) config_latest_types.Config {
	return config_latest.Merge(parent, w.config)
}
