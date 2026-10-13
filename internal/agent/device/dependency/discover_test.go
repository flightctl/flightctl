package dependency

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCRISourceReferencesContinuesLocalLookupsAfterRemoteBudgetExpires(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	repositoryDigestA := "sha256:" + strings.Repeat("e", 64)
	repositoryDigestB := "sha256:" + strings.Repeat("f", 64)
	platformDigest := "sha256:" + strings.Repeat("9", 64)
	repositoryRefA := "quay.io/local/base@" + repositoryDigestA
	repositoryRefB := "quay.io/local/base@" + repositoryDigestB
	sourceIndex, _ := testApplicationTargetIndex(t, platformDigest)

	exec := executer.NewMockExecuter(ctrl)
	imageList := `{"images":[{"repoDigests":["` + repositoryRefA + `","` + repositoryRefB + `"]}]}`
	imageListCall := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "crictl", "images", "--output", "json",
	).Return(imageList, "", 0)
	runtimeInfoCall := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "crictl", "version",
	).Return("RuntimeName: containerd\n", "", 0)
	firstLocalLookup := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "content", "get", repositoryDigestA,
	).Return("", "content not found", 1)
	remoteLookup := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+repositoryRefA, "--no-creds",
	).DoAndReturn(func(ctx context.Context, _ string, _ ...string) (string, string, int) {
		<-ctx.Done()
		return "", "remote inspection timed out", 1
	})
	secondLocalLookup := exec.EXPECT().ExecuteWithContext(
		gomock.Any(), "ctr", "--address", "/run/containerd/containerd.sock", "--namespace", "k8s.io", "content", "get", repositoryDigestB,
	).Return(sourceIndex, "", 0)
	gomock.InOrder(imageListCall, runtimeInfoCall, firstLocalLookup, remoteLookup, secondLocalLookup)

	root := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	cri := client.NewCRI(logger, exec, readWriter, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, readWriter)
	manager := &prefetchManager{log: logger, pullTimeout: time.Millisecond}

	sourceRefs, lookupErr := manager.criSourceReferences(
		context.Background(),
		cri,
		skopeo,
		[]string{platformDigest},
		client.WithCRIConfig(filepath.Join(root, "missing.yaml")),
	)
	require.NoError(lookupErr)
	require.Equal(map[string]string{platformDigest: repositoryRefB}, sourceRefs)
}
