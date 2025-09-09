package agentserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/samber/lo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type challengeVerificationError struct {
	message string
}

func (e *challengeVerificationError) Error() string {
	return e.message
}

// sendErrorResponse sends an error response message through the stream
func (s *AgentGrpcServer) sendErrorResponse(stream pb.EnrollmentRequestService_PerformTPMChallengeServer, message string) error {
	errorResp := &pb.ServerTPMChallengeMessage{
		Payload: &pb.ServerTPMChallengeMessage_Error{
			Error: &pb.TPMChallengeError{
				Message: message,
			},
		},
	}
	return stream.Send(errorResp)
}

// receiveAndValidateInitialRequest receives and validates the initial challenge request
func (s *AgentGrpcServer) receiveAndValidateInitialRequest(stream pb.EnrollmentRequestService_PerformTPMChallengeServer) (string, error) {
	req, err := stream.Recv()
	if err != nil {
		return "", status.Error(codes.Internal, "failed to receive challenge request")
	}

	challengeRequest := req.GetChallengeRequest()
	if challengeRequest == nil {
		return "", status.Error(codes.InvalidArgument, "expected challenge request")
	}

	enrollmentRequestName := challengeRequest.EnrollmentRequestName
	return enrollmentRequestName, nil
}

// validateEnrollmentRequest validates the enrollment request and parses the TPM CSR
func (s *AgentGrpcServer) validateEnrollmentRequest(ctx context.Context, stream pb.EnrollmentRequestService_PerformTPMChallengeServer, enrollmentRequestName string) (*api.EnrollmentRequest, *tpm.ParsedTCGCSR, error) {
	enrollmentRequest, status := s.service.GetEnrollmentRequest(ctx, enrollmentRequestName)
	if status.Code != http.StatusOK {
		if err := s.sendErrorResponse(stream, fmt.Sprintf("Enrollment request not found: %s", enrollmentRequestName)); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return nil, nil, fmt.Errorf("getting enrollment request %s: %s", enrollmentRequestName, status.Message)
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
		return nil, nil, fmt.Errorf("%s", errorMessage)
	}

	csrBytes, isTPM := tpm.ParseTCGCSRBytes(enrollmentRequest.Spec.Csr)
	if !isTPM {
		if err := s.sendErrorResponse(stream, "Not a valid TPM CSR"); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return nil, nil, fmt.Errorf("enrollment request %s does not contain a valid TPM CSR", enrollmentRequestName)
	}

	parsed, err := tpm.ParseTCGCSR(csrBytes)
	if err != nil {
		if sendErr := s.sendErrorResponse(stream, fmt.Sprintf("Not a valid TPM CSR: %v", err)); sendErr != nil {
			s.log.Errorf("Failed to send error response: %v", sendErr)
		}
		return nil, nil, fmt.Errorf("parsing TCG CSR for enrollment request %s: %w", enrollmentRequestName, err)
	}

	return enrollmentRequest, parsed, nil
}

// generateAndSendChallenge creates a credential challenge and sends it to the client
func (s *AgentGrpcServer) generateAndSendChallenge(stream pb.EnrollmentRequestService_PerformTPMChallengeServer, parsed *tpm.ParsedTCGCSR) (*tpm.CredentialChallenge, error) {
	challenge, err := tpm.CreateCredentialChallenge(parsed.CSRContents.Payload.EkCert, parsed.CSRContents.Payload.AttestPub)
	if err != nil {
		if sendErr := s.sendErrorResponse(stream, fmt.Sprintf("Failed to create challenge: %v", err)); sendErr != nil {
			s.log.Errorf("Failed to send error response: %v", sendErr)
		}
		return nil, fmt.Errorf("creating TPM credential challenge: %w", err)
	}

	challengeResp := &pb.ServerTPMChallengeMessage{
		Payload: &pb.ServerTPMChallengeMessage_Challenge{
			Challenge: &pb.TPMChallenge{
				CredentialBlob:  challenge.CredentialBlob,
				EncryptedSecret: challenge.EncryptedSecret,
			},
		},
	}

	if err := stream.Send(challengeResp); err != nil {
		return nil, fmt.Errorf("sending challenge: %w", err)
	}

	return challenge, nil
}

// receiveAndVerifyResponse receives the challenge response and verifies the secret
func (s *AgentGrpcServer) receiveAndVerifyResponse(stream pb.EnrollmentRequestService_PerformTPMChallengeServer, challenge *tpm.CredentialChallenge) error {
	responseMsg, err := stream.Recv()
	if err != nil {
		return status.Error(codes.Internal, "failed to receive challenge response")
	}

	challengeResponse := responseMsg.GetChallengeResponse()
	if challengeResponse == nil {
		return &challengeVerificationError{message: "Expected challenge response"}
	}

	if !bytes.Equal(challengeResponse.Secret, challenge.ExpectedSecret) {
		return &challengeVerificationError{message: "Challenge verification failed"}
	}

	return nil
}

// updateStatusToFailed updates the enrollment request status to failed and sends error response
func (s *AgentGrpcServer) updateStatusToFailed(ctx context.Context, stream pb.EnrollmentRequestService_PerformTPMChallengeServer, enrollmentRequest *api.EnrollmentRequest, enrollmentRequestName string, errorMessage string) error {
	condition := api.Condition{
		Type:    api.ConditionTypeEnrollmentRequestTPMVerified,
		Status:  api.ConditionStatusFalse,
		Reason:  api.TPMChallengeFailedReason,
		Message: fmt.Sprintf("TPM challenge failed: %s", errorMessage),
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)

	_, status := s.service.ReplaceEnrollmentRequestStatus(ctx, enrollmentRequestName, *enrollmentRequest)
	if status.Code != http.StatusOK {
		if err := s.sendErrorResponse(stream, errorMessage); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return fmt.Errorf("updating enrollment request %s: %s", enrollmentRequestName, status.Message)
	}

	// Always send error response to client when challenge verification fails
	if err := s.sendErrorResponse(stream, errorMessage); err != nil {
		s.log.Errorf("Failed to send error response: %v", err)
		return fmt.Errorf("sending error response: %w", err)
	}

	return nil
}

// updateStatusAndSendSuccess updates the enrollment request status and sends success response
func (s *AgentGrpcServer) updateStatusAndSendSuccess(ctx context.Context, stream pb.EnrollmentRequestService_PerformTPMChallengeServer, enrollmentRequest *api.EnrollmentRequest, enrollmentRequestName string) error {
	condition := api.Condition{
		Type:    api.ConditionTypeEnrollmentRequestTPMVerified,
		Status:  api.ConditionStatusTrue,
		Reason:  api.TPMChallengeSucceededReason,
		Message: "TPM activate credential challenge completed successfully",
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)

	_, status := s.service.ReplaceEnrollmentRequestStatus(ctx, enrollmentRequestName, *enrollmentRequest)
	if status.Code != http.StatusOK {
		if err := s.sendErrorResponse(stream, "Failed to update enrollment status"); err != nil {
			s.log.Errorf("Failed to send error response: %v", err)
		}
		return fmt.Errorf("updating enrollment request: %s status: %s", enrollmentRequestName, status.Message)
	}

	successResp := &pb.ServerTPMChallengeMessage{
		Payload: &pb.ServerTPMChallengeMessage_Success{
			Success: &pb.TPMChallengeComplete{
				Message: "TPM challenge completed successfully",
			},
		},
	}

	if err := stream.Send(successResp); err != nil {
		return fmt.Errorf("sending success response: %w", err)
	}

	return nil
}

// PerformTPMChallenge implements the TPM challenge-response protocol for device enrollment
func (s *AgentGrpcServer) PerformTPMChallenge(stream pb.EnrollmentRequestService_PerformTPMChallengeServer) error {
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
		var challengeErr *challengeVerificationError
		if errors.As(err, &challengeErr) {
			s.log.Errorf("TPM challenge verification failed for enrollment request %s: %s", enrollmentRequestName, challengeErr.message)
			return s.updateStatusToFailed(ctx, stream, enrollmentRequest, enrollmentRequestName, challengeErr.message)
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
