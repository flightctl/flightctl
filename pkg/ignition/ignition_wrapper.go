package ignition

import (
	"encoding/json"
	"path/filepath"
	"strconv"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/samber/lo"
	"github.com/vincent-petithory/dataurl"
)

const initialIgnition = `{"ignition": {"version": "3.4.0"}}`

type Wrapper interface {
	SetFile(filePath string, contents []byte, mode int, base64 bool, user string, group string)
	ChangeMountPath(mountPath string)
	AsJson() ([]byte, error)
	AsMap() (map[string]interface{}, error)
	AsIgnitionConfig() config_latest_types.Config
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

func NewWrapperFromJson(j []byte) (Wrapper, error) {
	var cfg config_latest_types.Config
	err := json.Unmarshal(j, &cfg)
	if err != nil {
		return nil, err
	}
	return &wrapper{
		config: cfg,
	}, nil
}

func NewWrapperFromIgnition(ign config_latest_types.Config) Wrapper {
	return &wrapper{config: ign}
}

func (w *wrapper) SetFile(filePath string, contents []byte, mode int, base64 bool, user string, group string) {
	file := config_latest_types.File{
		Node: config_latest_types.Node{
			Path:      filePath,
			Overwrite: lo.ToPtr(true),
			Group:     config_latest_types.NodeGroup{},
			User:      config_latest_types.NodeUser{Name: lo.ToPtr("root")},
		},
		FileEmbedded1: config_latest_types.FileEmbedded1{
			Contents: config_latest_types.Resource{},
			Mode:     &mode,
		},
	}

	if base64 {
		url := dataurl.New(contents, "text/plain;base64")
		url.Encoding = dataurl.EncodingASCII // Otherwise the library will double base64 encode
		file.FileEmbedded1.Contents.Source = lo.ToPtr(url.String())
	} else {
		file.FileEmbedded1.Contents.Source = lo.ToPtr(dataurl.New(contents, "text/plain").String())
	}
	if user != "" {
		file.Node.User = userStringToNodeUser(user)
	}
	if group != "" {
		file.Node.Group = groupStringToNodeGroup(group)
	}
	w.config.Storage.Files = append(w.config.Storage.Files, file)
}

func (w *wrapper) ChangeMountPath(mountPath string) {
	for i := range w.config.Storage.Files {
		w.config.Storage.Files[i].Node.Path = filepath.Join(mountPath, w.config.Storage.Files[i].Node.Path)
	}
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

func (w *wrapper) AsIgnitionConfig() config_latest_types.Config {
	return w.config
}

func (w *wrapper) Merge(parent config_latest_types.Config) config_latest_types.Config {
	return config_latest.Merge(parent, w.config)
}

func userStringToNodeUser(user string) config_latest_types.NodeUser {
	userConfig := config_latest_types.NodeUser{}
	userID, err := strconv.Atoi(user)
	if err != nil {
		userConfig.Name = &user
	} else {
		userConfig.ID = &userID
	}
	return userConfig
}

func groupStringToNodeGroup(group string) config_latest_types.NodeGroup {
	groupConfig := config_latest_types.NodeGroup{}
	groupID, err := strconv.Atoi(group)
	if err != nil {
		groupConfig.Name = &group
	} else {
		groupConfig.ID = &groupID
	}
	return groupConfig
}
