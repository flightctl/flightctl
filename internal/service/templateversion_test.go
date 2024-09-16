package service

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

func TestTemplateVersion(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Template Version")
}

var _ = Describe("TemplateVersionApproval", func() {
	var (
		ctrl                *gomock.Controller
		mockStore           *store.MockStore
		mockTemplateVersion *store.MockTemplateVersion
		mockCallbackManager *tasks.MockCallbackManager
		serviceHandler      *ServiceHandler
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockStore = store.NewMockStore(ctrl)
		mockTemplateVersion = store.NewMockTemplateVersion(ctrl)
		mockCallbackManager = tasks.NewMockCallbackManager(ctrl)
		mockStore.EXPECT().TemplateVersion().Return(mockTemplateVersion).AnyTimes()
		serviceHandler = NewServiceHandler(mockStore, mockCallbackManager, nil, logrus.New(), "", "", "")
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	It("should approve the template version successfully", func() {
		mockTemplateVersion.EXPECT().Get(context.TODO(), gomock.Any(), "my-fleet", "123").Return(&api.TemplateVersion{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("123"),
			},
			Status: &api.TemplateVersionStatus{},
		}, nil)

		approval := &api.TemplateVersionApproval{
			Approved:   true,
			ApprovedBy: lo.ToPtr("me"),
		}

		mockTemplateVersion.EXPECT().UpdateStatus(context.TODO(), store.NullOrgId, gomock.Any(), nil, gomock.Any()).
			DoAndReturn(func(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, _ *bool, _ store.TemplateVersionStoreCallback) error {
				Expect(api.IsStatusConditionTrue(resource.Status.Conditions, api.TemplateVersionApproved)).To(BeTrue())
				Expect(resource.Status.Approval).To(Equal(approval))
				return nil
			})

		ret, err := serviceHandler.ApproveTemplateVersion(context.TODO(), server.ApproveTemplateVersionRequestObject{
			Fleet: "my-fleet",
			Name:  "123",
			Body:  approval,
		})

		Expect(err).To(BeNil())
		Expect(ret).To(Equal(server.ApproveTemplateVersion200JSONResponse{}))
	})
	It("template version doesn't exit", func() {
		mockTemplateVersion.EXPECT().Get(context.TODO(), gomock.Any(), "my-fleet", "123").Return(nil, flterrors.ErrResourceNotFound)
		approval := &api.TemplateVersionApproval{
			Approved:   true,
			ApprovedBy: lo.ToPtr("me"),
		}
		ret, err := serviceHandler.ApproveTemplateVersion(context.TODO(), server.ApproveTemplateVersionRequestObject{
			Fleet: "my-fleet",
			Name:  "123",
			Body:  approval,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(ret).To(Equal(server.ApproveTemplateVersion404JSONResponse{}))
	})
	It("should reject the template version", func() {
		mockTemplateVersion.EXPECT().Get(context.TODO(), gomock.Any(), "my-fleet", "123").Return(&api.TemplateVersion{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("123"),
			},
			Status: &api.TemplateVersionStatus{},
		}, nil)

		approval := &api.TemplateVersionApproval{
			ApprovedBy: lo.ToPtr("me"),
		}

		mockTemplateVersion.EXPECT().UpdateStatus(context.TODO(), store.NullOrgId, gomock.Any(), nil, gomock.Any()).
			DoAndReturn(func(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, _ *bool, _ store.TemplateVersionStoreCallback) error {
				Expect(api.IsStatusConditionFalse(resource.Status.Conditions, api.TemplateVersionApproved)).To(BeTrue())
				Expect(resource.Status.Approval).To(Equal(approval))
				return nil
			})

		ret, err := serviceHandler.ApproveTemplateVersion(context.TODO(), server.ApproveTemplateVersionRequestObject{
			Fleet: "my-fleet",
			Name:  "123",
			Body:  approval,
		})

		Expect(err).To(BeNil())
		Expect(ret).To(Equal(server.ApproveTemplateVersion200JSONResponse{}))
	})
	It("should warn on approval of already approve template version", func() {
		approval := &api.TemplateVersionApproval{
			Approved:   true,
			ApprovedBy: lo.ToPtr("me"),
		}
		mockTemplateVersion.EXPECT().Get(context.TODO(), gomock.Any(), "my-fleet", "123").Return(&api.TemplateVersion{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("123"),
			},
			Status: &api.TemplateVersionStatus{
				Approval: approval,
				Conditions: []api.Condition{
					{
						Status: api.ConditionStatusTrue,
						Type:   api.TemplateVersionApproved,
					},
				},
			},
		}, nil)

		ret, err := serviceHandler.ApproveTemplateVersion(context.TODO(), server.ApproveTemplateVersionRequestObject{
			Fleet: "my-fleet",
			Name:  "123",
			Body:  approval,
		})

		Expect(err).To(BeNil())
		Expect(ret).To(Equal(server.ApproveTemplateVersion400JSONResponse{
			Message: "Template version is already approved",
		}))
	})
})
