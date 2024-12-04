package tasks

import (
	"context"
	"sync"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/sirupsen/logrus"
	"github.com/google/uuid"
)

const DefaultBatchSize = 100
const DefaultCSRExpiry = 7 * 24 *60
const DefaultEnrollExpiry = 365 * 24 * 60
var caMutex sync.Mutex

func asyncSign(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, log logrus.FieldLogger, ca crypto.CA) error {

	// Only one instance is run at any given time

	if !caMutex.TryLock() {
		return nil
	}
	defer caMutex.Unlock()

	// Run repeatedly until there are no records to process.
	// Process DefaultBatchSize at a time

	var count = 1

	for count > 0 {
		switch resourceRef.Op {
		case AsyncSignOpSignAll:
			 count, err := asyncSignEnrollment(ctx, resourceRef, store, callbackManager, log, ca)
			 if err != nil {
			     return err
			 }
			 extra, _ := asyncSignCSR(ctx, resourceRef, store, callbackManager, log, ca)
			 count += extra
		case AsyncSignOpSignCSR:
			 count, _ = asyncSignCSR(ctx, resourceRef, store, callbackManager, log, ca)
		case AsyncSignOpSignEnrollment:
			 count, _ = asyncSignEnrollment(ctx, resourceRef, store, callbackManager, log, ca)
		default:
			log.Errorf("asyncSign called with unexpected op %s", resourceRef.Op)
		}
	}
	return nil
}



func asyncSignEnrollment(ctx context.Context, resourceRef *ResourceReference, dbStore store.Store, callbackManager CallbackManager, log logrus.FieldLogger, ca crypto.CA) (int, error) {

	listParams := store.ListParams{Limit:DefaultBatchSize}
	selector, err := selector.Parse("!status.certificate")

	if err != nil {
		log.Errorf("SQL error while fetching enrollment requests to be signed", err)
		return 0, err
	}
	listParams.FieldSelector = selector

	orgId := store.NullOrgId
	ereqs, err := dbStore.EnrollmentRequest().List(ctx, orgId, listParams)
	if err != nil {
		log.Info(err)
		return 0, err
	}

	count := 0

	for _, ereq := range ereqs.Items {
		if ereq.Status != nil && ereq.Status.Certificate == nil && ereq.Status.Conditions != nil && v1alpha1.IsStatusConditionTrue(ereq.Status.Conditions, v1alpha1.EnrollmentRequestApproved) {
			csr, err := crypto.ParseCSR([]byte(ereq.Spec.Csr))
			if err := csr.CheckSignature(); err != nil {
				continue
			}
			csr.Subject.CommonName, err = crypto.CNFromDeviceFingerprint(*ereq.Metadata.Name)
			if err == nil {
				cert, err := ca.IssueRequestedClientCertificate(csr, DefaultEnrollExpiry)
				if err == nil {
					signed := string(cert)
					ereq.Status.Certificate = &signed
					_, err = dbStore.EnrollmentRequest().UpdateStatus(ctx, orgId, &ereq)
					if err != nil {
						log.Errorf("Failed to update enrollment request %s", err)
					} else {
						count++
					}
				}
			}
		}
	}
	return count, nil

}

func asyncSignCSR(ctx context.Context, resourceRef *ResourceReference, dbStore store.Store, callbackManager CallbackManager, log logrus.FieldLogger, ca crypto.CA) (int, error) {


	listParams := store.ListParams{Limit:DefaultBatchSize}
	selector, err := selector.Parse("!status.certificate")

	if err != nil {
		log.Errorf("SQL error while fetching csrs to be signed", err)
		return 0, err
	}
	listParams.FieldSelector = selector

	orgId := store.NullOrgId
	ereqs, err := dbStore.CertificateSigningRequest().List(ctx, orgId, listParams)
	if err != nil {
		log.Info(err)
		return 0, err
	}

	count := 0

	for _, ereq := range ereqs.Items {
		if ereq.Status == nil {
			ereq.Status = &v1alpha1.CertificateSigningRequestStatus{}
		}
		if ereq.Status.Certificate == nil {
			csr, err := crypto.ParseCSR([]byte(ereq.Spec.Request))
			if err != nil {
				continue
			}
			if err := csr.CheckSignature(); err != nil {
				continue
			}
			u := csr.Subject.CommonName
			if u == "" {
				u = uuid.NewString()
			}
			csr.Subject.CommonName, err = crypto.BootstrapCNFromName(u)
			if err != nil {
				continue
			}
			expiry := DefaultCSRExpiry
			if ereq.Spec.ExpirationSeconds != nil {
				expiry = int(*ereq.Spec.ExpirationSeconds)
			}
			cert, err := ca.IssueRequestedClientCertificate(csr, expiry)
			if err == nil {
				ereq.Status.Certificate = &cert
				_, err := dbStore.CertificateSigningRequest().UpdateStatus(ctx, orgId, &ereq)
				if err != nil {
					log.Errorf("Failed to update csr %s", err)
				} else {
					count++
				}
			}

		}
	}

	return count, nil
}
