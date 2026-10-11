package dependency

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApplicationDeltaCRIPullsWithoutDiscovery(t *testing.T) {
	for _, hint := range []string{"", "not an image", "quay.io/acme/delta:target"} {
		t.Run("When hint is "+hint+" it should full pull without discovery", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 1))
			target := imageRef{image: "quay.io/acme/app:latest", runtime: OCITypeCRIImage}
			task := &prefetchTask{ociType: OCITypeCRIImage, delta: &OCIDeltaTarget{Application: "app", Hint: hint}, deltaGeneration: 1}
			m := &prefetchManager{log: logger, cliClients: client.NewCLIClients(client.WithCRIClient(cri)), deltaStatusStore: deltastatus.NewInMemory(logger), tasks: map[imageRef]*prefetchTask{target: task}, deltaGeneration: 1}
			exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", target.image}, gomock.Any()).Return(strings.Repeat("f", 64), "", 0)
			require.NoError(t, m.pullCRIImage(context.Background(), target, task, nil))
			require.Empty(t, task.preparedDigest)
		})
	}
}
