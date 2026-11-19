package appinspection

import (
	"crypto/sha256"
	_ "crypto/sha512" // Register SHA-384 and SHA-512 for OCI digest validation.
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/validation"
)

type InputKey string

var inspectionPlatform = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*){1,2}$`)

// KeyForInput hashes canonical identity metadata for one organization.
func KeyForInput(orgID uuid.UUID, input model.ApplicationInspectionInput) (InputKey, error) {
	if err := validateInput(input); err != nil {
		return "", err
	}
	identity := struct {
		Version int
		OrgID   uuid.UUID
		Input   model.ApplicationInspectionInput
	}{1, orgID, input}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(encoded)
	return InputKey(hex.EncodeToString(key[:])), nil
}

func validateInput(input model.ApplicationInspectionInput) error {
	if err := validateProviderFormat(input); err != nil {
		return err
	}
	inline := input.InlineFingerprint != (model.InspectionFingerprint{})
	if input.Format == model.InspectionFormatInline {
		if !inline || input.ParentDigest != "" || input.Platform != "" {
			return errors.New("invalid inline inspection identity")
		}
		return nil
	}
	if inline || input.ParentDigest.Validate() != nil {
		return errors.New("invalid inspection parent digest")
	}
	if input.Platform != "" && (len(input.Platform) > 128 || !inspectionPlatform.MatchString(input.Platform)) {
		return errors.New("invalid inspection platform")
	}
	return nil
}

func validateProviderFormat(input model.ApplicationInspectionInput) error {
	switch input.Provider {
	case model.InspectionProviderHelm:
		if input.Format != model.InspectionFormatChart || input.Helm == nil {
			return errors.New("invalid Helm inspection identity")
		}
		if len(input.Helm.ReleaseName) > 53 || len(validation.IsDNS1123Subdomain(input.Helm.ReleaseName)) != 0 ||
			len(validation.IsDNS1123Label(input.Helm.Namespace)) != 0 {
			return errors.New("invalid Helm release identity")
		}
	case model.InspectionProviderCompose, model.InspectionProviderQuadlet:
		if input.Helm != nil {
			return errors.New("unexpected Helm inspection identity")
		}
		switch input.Format {
		case model.InspectionFormatInline, model.InspectionFormatRaw, model.InspectionFormatTar, model.InspectionFormatTarGz:
		default:
			return errors.New("invalid application inspection format")
		}
	default:
		return errors.New("invalid application inspection provider")
	}
	return nil
}
