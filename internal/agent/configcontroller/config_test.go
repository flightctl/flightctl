package configcontroller

import (
	// "encoding/json"
	// "encoding/json"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	// ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	// ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestXxx(t *testing.T) {
	require := require.New(t)

	deviceBytes, err := os.ReadFile(filepath.Join("testdata", "device.yaml"))
	require.NoError(err)

	d := &api.Device{}

	err = yaml.Unmarshal(deviceBytes, d)
	require.NoError(err)

	// for _, config := range *d.Spec.Config {
	// 	fmt.Printf("config: %+v\n", config)
	// }

	// fmt.Printf("device: %+v\n", d.Spec.Config[0])

	// var ignitionConfig ignv3types.Config

	// bytes, err := (*d.Spec.Config)[0].UnmarshalJSON()
	// require.NoError(err)

	r, err := (*d.Spec.Config)[0].MarshalJSON()
	require.NoError(err)

	// err = json.Unmarshal((*d.Spec.Config)[0].MarshalJSON(), &ignitionConfig)
	// require.NoError(err)

	fmt.Printf("ignitionConfig: %+v\n", string(r))

	var ignition Ignition

	err = json.Unmarshal(r, &ignition)
	require.NoError(err)

	fmt.Printf("config: %s\n", ignition.Raw)

}
