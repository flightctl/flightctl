package store_test

import (
	"context"
	"crypto"
	"encoding/base32"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

// GenerateDeviceNameAndCSR generates a device name (deterministic, like the agent) together
// with a matching PEM-encoded CSR using the same keypair. This is reused by multiple tests.
func GenerateDeviceNameAndCSR() (string, []byte) {
	publicKey, privateKey, err := fcrypto.NewKeyPair()
	Expect(err).ToNot(HaveOccurred())

	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	Expect(err).ToNot(HaveOccurred())

	deviceName := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))

	csrPEM, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), deviceName)
	Expect(err).ToNot(HaveOccurred())

	return deviceName, csrPEM
}

var _ = Describe("EnrollmentRequest store restore operations", func() {
	var (
		log            *logrus.Logger
		ctx            context.Context
		orgId          uuid.UUID
		storeInst      store.Store
		cfg            *config.Config
		dbName         string
		serviceHandler service.Service
		ctrl           *gomock.Controller
	)

	BeforeEach(func() {
		ctx = util.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)

		// Use the default organization ID that the service layer expects
		orgId = store.NullOrgId

		// Setup service handler for proper approval
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer := queues.NewMockQueueProducer(ctrl)
		workerClient := worker_client.NewWorkerClient(mockQueueProducer, log)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())

		// Setup CA for enrollment requests
		testDirPath := GinkgoT().TempDir()
		caCfg := ca.NewDefault(testDirPath)
		caClient, _, err := icrypto.EnsureCA(caCfg)
		Expect(err).ToNot(HaveOccurred())

		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, caClient, log, "", "", []string{})
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
		if ctrl != nil {
			ctrl.Finish()
		}
	})

	Context("PrepareEnrollmentRequestsAfterRestore", func() {
		It("should annotate non-approved enrollment requests with awaitingReconnect", func() {
			// Create test enrollment requests with different approval states
			erStore := storeInst.EnrollmentRequest()

			// Generate valid CSR data for enrollment requests
			nonApprovedName, nonApprovedCSR := GenerateDeviceNameAndCSR()
			toApproveName, toApproveCSR := GenerateDeviceNameAndCSR()
			alreadyAnnotatedName, alreadyAnnotatedCSR := GenerateDeviceNameAndCSR()

			// Create non-approved enrollment request
			nonApprovedER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(nonApprovedName),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: string(nonApprovedCSR),
				},
			}

			// Create enrollment request to be approved
			toApproveER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(toApproveName),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: string(toApproveCSR),
				},
			}

			// Create enrollment request with awaitingReconnect annotation already set
			alreadyAnnotatedER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(alreadyAnnotatedName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
					},
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: string(alreadyAnnotatedCSR),
				},
			}

			// Create the enrollment requests using the service layer
			_, st := serviceHandler.CreateEnrollmentRequest(ctx, nonApprovedER)
			Expect(st.Code).To(BeEquivalentTo(201))

			_, st = serviceHandler.CreateEnrollmentRequest(ctx, toApproveER)
			Expect(st.Code).To(BeEquivalentTo(201))

			// Create the enrollment request using the service layer with internal request context
			// This will preserve annotations since fromAPI=false for internal requests
			internalCtx := context.WithValue(ctx, consts.InternalRequestCtxKey, true)
			_, st = serviceHandler.CreateEnrollmentRequest(internalCtx, alreadyAnnotatedER)
			Expect(st.Code).To(BeEquivalentTo(201))

			// Verify the annotation was preserved
			By("Debug: Verifying annotation was preserved")
			createdER, err := erStore.Get(ctx, orgId, *alreadyAnnotatedER.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			By(fmt.Sprintf("Debug: Created ER annotations: %+v", createdER.Metadata.Annotations))

			// Approve one enrollment request using the service layer
			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, []string{}, nil)
			ctxApproval := context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st = serviceHandler.ApproveEnrollmentRequest(ctxApproval, toApproveName, approval)
			Expect(st.Code).To(BeEquivalentTo(200))

			// Debug: Print all enrollment requests and their status
			By("Debug: Listing all enrollment requests before PrepareEnrollmentRequestsAfterRestore")
			allERs, err := erStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())

			for i, er := range allERs.Items {
				By(fmt.Sprintf("ER %d: Name=%s, Status=%+v, Annotations=%+v", i, *er.Metadata.Name, er.Status, er.Metadata.Annotations))

				// Check if this ER has the awaitingReconnect annotation
				if er.Metadata.Annotations != nil {
					if awaitingReconnect, exists := (*er.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]; exists {
						By(fmt.Sprintf("  -> Has awaitingReconnect annotation: %s", awaitingReconnect))
					} else {
						By("  -> No awaitingReconnect annotation")
					}
				} else {
					By("  -> No annotations at all")
				}
			}

			// Call PrepareEnrollmentRequestsAfterRestore
			updatedCount, err := erStore.PrepareEnrollmentRequestsAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(1)), "Should update only the non-approved enrollment request")

			// Verify the non-approved enrollment request got the annotation
			updatedNonApproved, err := erStore.Get(ctx, orgId, nonApprovedName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedNonApproved.Metadata.Annotations).ToNot(BeNil())
			Expect(*updatedNonApproved.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))

			// Verify the approved enrollment request was not updated
			updatedApproved, err := erStore.Get(ctx, orgId, toApproveName)
			Expect(err).ToNot(HaveOccurred())
			// Annotations should be empty (not nil) since it was created through service layer
			Expect(updatedApproved.Metadata.Annotations).ToNot(BeNil())
			Expect(*updatedApproved.Metadata.Annotations).To(BeEmpty())

			// Verify the already annotated enrollment request was not updated again
			updatedAlreadyAnnotated, err := erStore.Get(ctx, orgId, alreadyAnnotatedName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedAlreadyAnnotated.Metadata.Annotations).ToNot(BeNil())
			Expect(*updatedAlreadyAnnotated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should handle enrollment requests with nil status", func() {
			erStore := storeInst.EnrollmentRequest()

			// Generate valid CSR data for enrollment request
			nilStatusName, nilStatusCSR := GenerateDeviceNameAndCSR()

			// Create enrollment request with nil status (default state)
			nilStatusER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(nilStatusName),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: string(nilStatusCSR),
				},
			}

			_, st := serviceHandler.CreateEnrollmentRequest(ctx, nilStatusER)
			Expect(st.Code).To(BeEquivalentTo(201))

			// Call PrepareEnrollmentRequestsAfterRestore
			updatedCount, err := erStore.PrepareEnrollmentRequestsAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(1)), "Should update enrollment request with nil status")

			// Verify it got the annotation
			updated, err := erStore.Get(ctx, orgId, nilStatusName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*updated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should handle enrollment requests with nil approval", func() {
			erStore := storeInst.EnrollmentRequest()

			// Generate valid CSR data for enrollment request
			nilApprovalName, nilApprovalCSR := GenerateDeviceNameAndCSR()

			// Create enrollment request with nil approval (default state)
			nilApprovalER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(nilApprovalName),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: string(nilApprovalCSR),
				},
			}

			_, st := serviceHandler.CreateEnrollmentRequest(ctx, nilApprovalER)
			Expect(st.Code).To(BeEquivalentTo(201))

			// Call PrepareEnrollmentRequestsAfterRestore
			updatedCount, err := erStore.PrepareEnrollmentRequestsAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(1)), "Should update enrollment request with nil approval")

			// Verify it got the annotation
			updated, err := erStore.Get(ctx, orgId, nilApprovalName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.Metadata.Annotations).ToNot(BeNil())
			Expect(*updated.Metadata.Annotations).To(HaveKeyWithValue(api.DeviceAnnotationAwaitingReconnect, "true"))
		})

		It("should return zero when no enrollment requests need updating", func() {
			erStore := storeInst.EnrollmentRequest()

			// Generate valid CSR data for enrollment request
			toApproveName, toApproveCSR := GenerateDeviceNameAndCSR()

			// Create enrollment request to be approved
			toApproveER := api.EnrollmentRequest{
				ApiVersion: api.EnrollmentRequestAPIVersion,
				Kind:       api.EnrollmentRequestKind,
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(toApproveName),
				},
				Spec: api.EnrollmentRequestSpec{
					Csr: string(toApproveCSR),
				},
			}

			_, st := serviceHandler.CreateEnrollmentRequest(ctx, toApproveER)
			Expect(st.Code).To(BeEquivalentTo(201))

			// Approve the enrollment request using the service layer
			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, []string{}, nil)
			ctxApproval := context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)

			approval := api.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &map[string]string{"approved": "true"},
			}

			_, st = serviceHandler.ApproveEnrollmentRequest(ctxApproval, toApproveName, approval)
			Expect(st.Code).To(BeEquivalentTo(200))

			// Call PrepareEnrollmentRequestsAfterRestore
			updatedCount, err := erStore.PrepareEnrollmentRequestsAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedCount).To(Equal(int64(0)), "Should not update any approved enrollment requests")
		})
	})
})
