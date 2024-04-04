package tasks_test

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var repo model.Repository = model.Repository{
	Spec: &model.JSONField[api.RepositorySpec]{
		Data: api.RepositorySpec{
			// This should move to E2E with a local git repo in kind
			Repo: util.StrToPtr("https://github.com/flightctl/flightctl"),
		},
	},
}
var _ = Describe("ResourceSync CloneGitRepo", Ordered, func() {

	It("should checkout a known git repo", func() {
		// Clone the repo
		fs, _, err := tasks.CloneGitRepo(&repo, nil, util.IntToPtr(1))
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
