package tasks_test

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("ResourceSync CloneGitRepo", Ordered, func() {

	It("should checkout a known git repo", func() {
		spec := api.RepositorySpec{}
		err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
			// This should move to E2E with a local git repo in kind
			Url: "https://github.com/flightctl/flightctl",
		})
		Expect(err).ToNot(HaveOccurred())
		repo := model.Repository{
			Spec: &model.JSONField[api.RepositorySpec]{
				Data: spec,
			},
		}

		// Clone the repo
		fs, _, err := tasks.CloneGitRepo(&repo, nil, lo.ToPtr(1))
		Expect(err).ToNot(HaveOccurred())

		err = fs.MkdirAll("/fleets", 0666)
		Expect(err).ToNot(HaveOccurred())

		fleet1, err := fs.Open("/examples/fleet.yaml")
		Expect(err).ToNot(HaveOccurred())
		defer fleet1.Close()
		fleet2, err := fs.Open("/examples/fleet-b.yaml")
		Expect(err).ToNot(HaveOccurred())
		defer fleet2.Close()

	})

})
