package agentserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// classifyStreamRecvError converts stream.Recv() errors to appropriate gRPC status codes
func classifyStreamRecvError(err error, contextMsg string) error {
	// Client closed the stream normally
	if errors.Is(err, io.EOF) {
		return status.Errorf(codes.Canceled, "%s: client closed stream", contextMsg)
	}

	// Check if it's already a gRPC status error and preserve it
	if s, ok := status.FromError(err); ok {
		return status.Errorf(s.Code(), "%s: %s", contextMsg, s.Message())
	}

	// Check for context cancellation
	if errors.Is(err, context.Canceled) {
		return status.Errorf(codes.Canceled, "%s: request cancelled", contextMsg)
	}

	// Check for context deadline exceeded
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.DeadlineExceeded, "%s: request timed out", contextMsg)
	}

	// Default to internal error for unexpected cases
	return status.Errorf(codes.Internal, "%s: unexpected error: %v", contextMsg, err)
}

// wrapInternalError wraps server-side errors as gRPC Internal errors
func wrapInternalError(err error, contextMsg string) error {
	return status.Errorf(codes.Internal, "%s: %v", contextMsg, err)
}

// wrapValidationError wraps client input validation errors as gRPC InvalidArgument errors
func wrapValidationError(err error, contextMsg string) error {
	return status.Errorf(codes.InvalidArgument, "%s: %v", contextMsg, err)
}

// updateEnrollmentRequestStatus updates the enrollment request status and handles errors consistently
func (s *AgentGrpcServer) updateEnrollmentRequestStatus(
	ctx context.Context,
	stream pb.Enrollment_TPMChallengeServer,
	enrollmentRequestName string,
	enrollmentRequest *api.EnrollmentRequest,
	streamErrorMessage string,
) error {
	orgId, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		orgId = store.NullOrgId
	}
	_, responseStatus := s.service.ReplaceEnrollmentRequestStatus(ctx, orgId, enrollmentRequestName, *enrollmentRequest)
	if responseStatus.Code != http.StatusOK {
		if err := s.sendErrorResponse(stream, streamErrorMessage); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return wrapInternalError(fmt.Errorf("updating enrollment request %s: %s", enrollmentRequestName, responseStatus.Message), "failed to update enrollment request status")
	}
	return nil
}

// sendErrorResponse sends an error response message through the stream
func (s *AgentGrpcServer) sendErrorResponse(stream pb.Enrollment_TPMChallengeServer, message string) error {
	errorResp := &pb.ServerChallenge{
		Payload: &pb.ServerChallenge_Error{
			Error: &pb.ChallengeError{
				Message: message,
			},
		},
	}
	return stream.Send(errorResp)
}

// receiveAndValidateInitialRequest receives and validates the initial challenge request
func (s *AgentGrpcServer) receiveAndValidateInitialRequest(stream pb.Enrollment_TPMChallengeServer) (string, error) {
	req, err := stream.Recv()
	if err != nil {
		return "", classifyStreamRecvError(err, "failed to receive challenge request")
	}

	challengeRequest := req.GetChallengeRequest()
	if challengeRequest == nil {
		return "", status.Error(codes.InvalidArgument, "expected challenge request")
	}

	enrollmentRequestName := challengeRequest.EnrollmentRequestName
	return enrollmentRequestName, nil
}

// validateEnrollmentRequest validates the enrollment request and parses the TPM CSR
func (s *AgentGrpcServer) validateEnrollmentRequest(ctx context.Context, stream pb.Enrollment_TPMChallengeServer, enrollmentRequestName string) (*api.EnrollmentRequest, *tpm.ParsedTCGCSR, error) {
	orgId, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		orgId = store.NullOrgId
	}
	enrollmentRequest, responseStatus := s.service.GetEnrollmentRequest(ctx, orgId, enrollmentRequestName)
	if responseStatus.Code != http.StatusOK {
		if err := s.sendErrorResponse(stream, fmt.Sprintf("Enrollment request not found: %s", enrollmentRequestName)); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		if responseStatus.Code == http.StatusNotFound {
			return nil, nil, status.Errorf(codes.NotFound, "enrollment request not found: %s", enrollmentRequestName)
		}
		return nil, nil, wrapInternalError(fmt.Errorf("getting enrollment request %s: %s", enrollmentRequestName, responseStatus.Message), "failed to retrieve enrollment request")
	}

	tpmCond := api.FindStatusCondition(lo.FromPtr(enrollmentRequest.Status).Conditions, api.ConditionTypeEnrollmentRequestTPMVerified)
	// 1. There should be a tpm verified condition at this point
	// 2. The enrollment request should not already be verified
	// 3. If the reason for it not being verified is that it failed initial verification then no more checks should be allowed
	if tpmCond == nil || tpmCond.Status == api.ConditionStatusTrue || tpmCond.Reason == api.TPMVerificationFailedReason {
		errorMessage := fmt.Sprintf("invalid enrollment request condition state %s: %v", enrollmentRequestName, tpmCond)
		if err := s.sendErrorResponse(stream, errorMessage); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return nil, nil, status.Errorf(codes.FailedPrecondition, "invalid enrollment request condition state %s: %v", enrollmentRequestName, tpmCond)
	}

	csrBytes, isTPM := tpm.ParseTCGCSRBytes(enrollmentRequest.Spec.Csr)
	if !isTPM {
		if err := s.sendErrorResponse(stream, "Not a valid TPM CSR"); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return nil, nil, status.Errorf(codes.InvalidArgument, "enrollment request %s does not contain a valid TPM CSR", enrollmentRequestName)
	}

	parsed, err := tpm.ParseTCGCSR(csrBytes)
	if err != nil {
		if sendErr := s.sendErrorResponse(stream, fmt.Sprintf("Not a valid TPM CSR: %v", err)); sendErr != nil {
			s.log.Errorf("Failed to send error response: %v", sendErr)
		}
		return nil, nil, wrapValidationError(err, fmt.Sprintf("parsing TCG CSR for enrollment request %s", enrollmentRequestName))
	}

	return enrollmentRequest, parsed, nil
}

// generateAndSendChallenge creates a credential challenge and sends it to the client
func (s *AgentGrpcServer) generateAndSendChallenge(stream pb.Enrollment_TPMChallengeServer, parsed *tpm.ParsedTCGCSR) (*tpm.CredentialChallenge, error) {
	challenge, err := tpm.CreateCredentialChallenge(parsed.CSRContents.Payload.EkCert, parsed.CSRContents.Payload.AttestPub)
	if err != nil {
		if sendErr := s.sendErrorResponse(stream, fmt.Sprintf("Failed to create challenge: %v", err)); sendErr != nil {
			s.log.Errorf("Failed to send error response: %v", sendErr)
		}
		return nil, wrapInternalError(err, "creating TPM credential challenge")
	}

	challengeResp := &pb.ServerChallenge{
		Payload: &pb.ServerChallenge_Challenge{
			Challenge: &pb.Challenge{
				CredentialBlob:  challenge.CredentialBlob,
				EncryptedSecret: challenge.EncryptedSecret,
			},
		},
	}

	if err := stream.Send(challengeResp); err != nil {
		return nil, wrapInternalError(err, "sending challenge")
	}

	return challenge, nil
}

// receiveAndVerifyResponse receives the challenge response and verifies the secret
func (s *AgentGrpcServer) receiveAndVerifyResponse(stream pb.Enrollment_TPMChallengeServer, challenge *tpm.CredentialChallenge) error {
	responseMsg, err := stream.Recv()
	if err != nil {
		return classifyStreamRecvError(err, "failed to receive challenge response")
	}

	challengeResponse := responseMsg.GetChallengeResponse()
	if challengeResponse == nil {
		return status.Error(codes.InvalidArgument, "Expected challenge response")
	}

	if !bytes.Equal(challengeResponse.Secret, challenge.ExpectedSecret) {
		return status.Error(codes.PermissionDenied, "Challenge verification failed")
	}

	return nil
}

// updateStatusToFailed updates the enrollment request status to failed and sends error response
func (s *AgentGrpcServer) updateStatusToFailed(ctx context.Context, stream pb.Enrollment_TPMChallengeServer, enrollmentRequest *api.EnrollmentRequest, enrollmentRequestName string, errorMessage string) error {
	condition := api.Condition{
		Type:    api.ConditionTypeEnrollmentRequestTPMVerified,
		Status:  api.ConditionStatusFalse,
		Reason:  api.TPMChallengeFailedReason,
		Message: fmt.Sprintf("TPM challenge failed: %s", errorMessage),
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)

	if err := s.updateEnrollmentRequestStatus(ctx, stream, enrollmentRequestName, enrollmentRequest, errorMessage); err != nil {
		return err
	}

	// Always send error response to client when challenge verification fails
	if err := s.sendErrorResponse(stream, errorMessage); err != nil {
		s.log.Errorf("Failed to send error response: %v", err)
		return wrapInternalError(err, "sending error response")
	}

	return nil
}

// updateStatusAndSendSuccess updates the enrollment request status and sends success response
func (s *AgentGrpcServer) updateStatusAndSendSuccess(ctx context.Context, stream pb.Enrollment_TPMChallengeServer, enrollmentRequest *api.EnrollmentRequest, enrollmentRequestName string) error {
	condition := api.Condition{
		Type:    api.ConditionTypeEnrollmentRequestTPMVerified,
		Status:  api.ConditionStatusTrue,
		Reason:  api.TPMChallengeSucceededReason,
		Message: "TPM activate credential challenge completed successfully",
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)

	if err := s.updateEnrollmentRequestStatus(ctx, stream, enrollmentRequestName, enrollmentRequest, "Failed to update enrollment status"); err != nil {
		return err
	}

	successResp := &pb.ServerChallenge{
		Payload: &pb.ServerChallenge_Success{
			Success: &pb.ChallengeComplete{
				Message: "TPM challenge completed successfully",
			},
		},
	}

	if err := stream.Send(successResp); err != nil {
		return wrapInternalError(err, "sending success response")
	}

	return nil
}

// TPMChallenge implements the TPM challenge-response protocol for device enrollment
func (s *AgentGrpcServer) TPMChallenge(stream pb.Enrollment_TPMChallengeServer) error {
	ctx := stream.Context()

	enrollmentRequestName, err := s.receiveAndValidateInitialRequest(stream)
	if err != nil {
		s.log.Errorf("Failed to receive and validate initial TPM challenge request: %v", err)
		return err
	}

	s.log.Debugf("Starting TPM challenge for enrollment request: %s", enrollmentRequestName)

	enrollmentRequest, parsed, err := s.validateEnrollmentRequest(ctx, stream, enrollmentRequestName)
	if err != nil {
		s.log.Errorf("Failed to validate enrollment request %s: %v", enrollmentRequestName, err)
		return err
	}

	challenge, err := s.generateAndSendChallenge(stream, parsed)
	if err != nil {
		s.log.Errorf("Failed to generate and send TPM challenge for enrollment request %s: %v", enrollmentRequestName, err)
		return err
	}

	if err := s.receiveAndVerifyResponse(stream, challenge); err != nil {
		// Check if it's a challenge verification failure (PermissionDenied or InvalidArgument)
		if st, ok := status.FromError(err); ok && (st.Code() == codes.PermissionDenied || st.Code() == codes.InvalidArgument) {
			s.log.Errorf("TPM challenge verification failed for enrollment request %s: %s", enrollmentRequestName, st.Message())
			return s.updateStatusToFailed(ctx, stream, enrollmentRequest, enrollmentRequestName, st.Message())
		}
		s.log.Errorf("Failed to receive and verify TPM challenge response for enrollment request %s: %v", enrollmentRequestName, err)
		return err
	}

	if err := s.updateStatusAndSendSuccess(ctx, stream, enrollmentRequest, enrollmentRequestName); err != nil {
		s.log.Errorf("Failed to update status and send success for enrollment request %s: %v", enrollmentRequestName, err)
		return err
	}

	s.log.Debugf("TPM challenge completed successfully for enrollment request: %s", enrollmentRequestName)
	return nil
}
