package firmwareupdater_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func fixtureReference(role string) string {
	return state.Registry + "/" + repository + "@" + state.Fixtures[role].Digest
}

var _ = Describe("Publisher signature verification at pull", func() {
	// pullWithApprovedPolicy pulls ref into fresh storage under the signature
	// policy rendered for the approved release of the test repository. The
	// registry requires authentication, so the test auth file is passed too.
	pullWithApprovedPolicy := func(ctx SpecContext, ref string) (*client.Podman, error) {
		dir := tempDir()
		recorder := newRecordingExecuter(podmanEnv(newStorage(filepath.Join(dir, "storage")), state.DiscoveryHome))
		rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
		podman := client.NewPodman(log.NewPrefixLogger("firmware-it"), recorder, rw, poll.Config{BaseDelay: time.Second, Factor: 1, MaxSteps: 1})
		d, err := policy.DecodeDescriptor(state.Fixtures[roleValid].Binding)
		Expect(err).NotTo(HaveOccurred())
		data, err := firmware.SignaturePolicy(d)
		Expect(err).NotTo(HaveOccurred())
		policyPath := filepath.Join(dir, "signature-policy.json")
		Expect(os.WriteFile(policyPath, data, 0o600)).To(Succeed())
		_, err = podman.Pull(ctx, ref, client.WithSignaturePolicy(policyPath), client.WithPullSecret(state.AuthFile))
		return podman, err
	}

	It("When the image is signed with the approved key for the exact repository it should admit it", func(ctx SpecContext) {
		ref := fixtureReference(roleValid)
		podman, err := pullWithApprovedPolicy(ctx, ref)
		Expect(err).NotTo(HaveOccurred())
		digest, err := podman.ImageDigest(ctx, ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(digest).To(Equal(state.Fixtures[roleValid].Digest))
	})

	DescribeTable("rejected images",
		func(ctx SpecContext, ref func() string) {
			podman, err := pullWithApprovedPolicy(ctx, ref())
			Expect(err).To(MatchError(ContainSubstring("Source image rejected")))
			Expect(podman.ImageExists(ctx, ref())).To(BeFalse(), "a rejected image must not be stored under the reference")
		},
		Entry("When the image is unsigned it should reject it", func() string { return fixtureReference(roleUnsigned) }),
		Entry("When the image is signed by an unknown key it should reject it", func() string { return fixtureReference(roleUnknownKey) }),
		Entry("When the signature identity names another repository it should reject it", func() string { return fixtureReference(roleOtherIdentity) }),
		Entry("When the image comes from an unrelated repository it should reject it by default", func() string {
			return state.Registry + "/" + otherRepository + "@" + state.ElsewhereDigest
		}),
	)
})
