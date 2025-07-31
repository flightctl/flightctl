package cli

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ccoveille/go-safecast"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/config"
	signer "github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/util/validation"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

const secondsInDay int = 24 * 60 * 60
const agentPath = "certs/"
const maxAttempts = 3

type CertificateOptions struct {
	GlobalOptions
	Name       string
	Expiration string
	Output     string
	OutputDir  string
	EncryptKey bool
	SignerName string
}

func DefaultCertificateOptions() *CertificateOptions {
	return &CertificateOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Name:          "",
		Expiration:    "7d",
		Output:        "embedded",
		OutputDir:     ".",
		EncryptKey:    false,
		SignerName:    "flightctl.io/enrollment",
	}
}

func NewCmdCertificate() *cobra.Command {
	o := DefaultCertificateOptions()
	cmd := &cobra.Command{
		Use: "certificate request [flags]",
		// more subcommands may be added later
		Short:     "Request a new certificate for a device with 'certificate request'",
		Args:      cobra.MatchAll(cobra.MinimumNArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"request"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.Run(ctx, args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *CertificateOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.Name, "name", "n", o.Name, "Specify a name for the certificate signing request.")
	fs.StringVarP(&o.Expiration, "expiration", "x", o.Expiration, "Specify desired certificate expiration in days, example: 7d.")
	fs.StringVarP(&o.Output, "output", "o", o.Output, "Specify desired output format for an enrollment cert: either 'reference' to have the config file reference key and cert file paths, or 'embedded' to have the key and cert embedded in the config file.")
	fs.StringVarP(&o.OutputDir, "output-dir", "d", o.OutputDir, "Specify desired output directory for key, cert, and ca files.")
	fs.StringVarP(&o.SignerName, "signer", "s", o.SignerName, "Specify the signer of the certificate request.")
	fs.BoolVarP(&o.EncryptKey, "encrypt", "e", o.EncryptKey, "Option to encrypt key file with a password from env var $FCPASS, or if $FCPASS is not set password must be provided during runtime.")
}

func (o *CertificateOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	if o.SignerName == "enrollment" {
		o.SignerName = "flightctl.io/enrollment"
	}

	if len(o.Name) == 0 {
		if o.SignerName == "flightctl.io/enrollment" {
			o.Name = "client-enrollment"
		} else {
			o.Name = "cert"
		}
	}
	return nil
}

func (o *CertificateOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	if errs := validation.ValidateResourceName(&o.Name); len(errs) > 0 {
		return fmt.Errorf("invalid resource name: %s", errors.Join(errs...).Error())
	}

	if errs := validation.ValidateSignerName(o.SignerName); len(errs) > 0 {
		return fmt.Errorf("invalid signer name %q: %w", o.SignerName, errors.Join(errs...))
	}

	// check if user updated output format while requesting a cert that is not an enrollment cert -
	// output format is only relevant for enrollment certs
	if o.SignerName != "flightctl.io/enrollment" && len(o.Output) > 0 {
		return fmt.Errorf("output format cannot be set for certificate types other than 'enrollment'")
	}

	re := `^\d+d$`
	matched, err := regexp.MatchString(re, o.Expiration)
	if err != nil || !matched {
		return fmt.Errorf("invalid expiration specified: %s\nexpiration must be in the form of days, example: 365d", o.Expiration)
	}

	return nil
}

func (o *CertificateOptions) Run(ctx context.Context, args []string) error {
	keypath := filepath.Join(o.OutputDir, o.Name+".key")
	fmt.Fprintf(os.Stderr, "Creating new ECDSA key pair and writing to %q.\n", keypath)
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return fmt.Errorf("creating new key pair: %w", err)
	}

	// write out the key asap - in case process is interrupted, key is needed to access
	// status of CSR via enrollmentconfig API
	if !o.EncryptKey {
		err = fccrypto.WriteKey(keypath, priv)
		if err != nil {
			return fmt.Errorf("writing private key to %s: %w", keypath, err)
		}
	} else {
		pw, err := getPassword()
		if err != nil {
			return fmt.Errorf("getting password: %w", err)
		}
		err = fccrypto.WritePasswordEncryptedKey(keypath, priv, pw)
		if err != nil {
			return fmt.Errorf("writing encrypted private key to %s: %w", keypath, err)
		}
	}

	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	csrName, err := o.submitCsrWithRetries(ctx, c, priv)
	if err != nil {
		return err
	}

	// if this isn't an enrollment cert, approval may take arbitrary time, so don't poll for
	// the cert here - we're done!
	if o.SignerName != "flightctl.io/enrollment" {
		return nil
	}

	fmt.Fprintf(os.Stderr, "Waiting for certificate to be approved and issued...")
	var currentCsr *api.CertificateSigningRequest
	err = wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, false, func(ctx context.Context) (bool, error) {
		fmt.Fprint(os.Stderr, ".")
		currentCsr, err = getCsr(csrName, c, ctx)
		if err != nil {
			return false, fmt.Errorf("reading CSR %q: %w", ctx.Value("name"), err)
		}
		return checkCsrCertReady(currentCsr), nil
	})
	switch {
	case err == nil:
		fmt.Fprintln(os.Stderr, " success.")
	case wait.Interrupted(err):
		return fmt.Errorf("timeout polling for certificate")
	default:
		return fmt.Errorf("polling for certificate: %w", err)
	}

	// get URIs and other data from enrollmentconfig API
	response, err := c.GetEnrollmentConfigWithResponse(ctx, nil)
	if err != nil {
		return fmt.Errorf("getting enrollment config: %w", err)
	}
	if err := validateHttpResponse(response.Body, response.StatusCode(), http.StatusOK); err != nil {
		return fmt.Errorf("getting enrollment config: %w", err)
	}

	// write out agent cert
	err = os.WriteFile(filepath.Join(o.OutputDir, o.Name+".crt"), *currentCsr.Status.Certificate, 0600)
	if err != nil {
		return fmt.Errorf("writing cert file: %w", err)
	}

	// write out ca bundle
	cadata, err := base64.StdEncoding.DecodeString(response.JSON200.EnrollmentService.Service.CertificateAuthorityData)
	if err != nil {
		return fmt.Errorf("base64 decoding CA data: %w", err)
	}
	err = os.WriteFile(filepath.Join(o.OutputDir, "ca.crt"), cadata, 0600)
	if err != nil {
		return fmt.Errorf("writing CA cert file: %w", err)
	}

	switch o.Output {
	case "embedded":
		err = createEmbeddedConfig(currentCsr, priv, response)
	case "reference":
		err = createReferenceConfig(o.Name, response)
	default:
	}
	if err != nil {
		return fmt.Errorf("creating output as %q: %w", o.Output, err)
	}

	return nil
}

func (o *CertificateOptions) submitCsrWithRetries(ctx context.Context, c *apiclient.ClientWithResponses, priv crypto.PrivateKey) (string, error) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		csrName := createUniqueName(o.Name)
		csrOrg := o.GetEffectiveOrganization()
		csrResourceJSON, err := createCsr(o, csrName, csrOrg, priv)
		if err != nil {
			return "", fmt.Errorf("creating CSR resource: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Submitting certificate signing request %q...", csrName)
		response, err := c.CreateCertificateSigningRequestWithBodyWithResponse(ctx, "application/json", bytes.NewReader(csrResourceJSON))
		if err != nil {
			return "", fmt.Errorf("submitting CSR: %w", err)
		}
		if response.HTTPResponse == nil {
			return "", fmt.Errorf("submitting CSR: empty HTTP response")
		}
		switch response.HTTPResponse.StatusCode {
		case http.StatusCreated:
			fmt.Fprintln(os.Stderr, " success.")
			return csrName, nil
		case http.StatusConflict:
			fmt.Fprintln(os.Stderr, " failed because a CSR with that name already exists.")
			continue
		default:
			fmt.Fprintln(os.Stderr, " failed.")
			if response.JSON400 != nil {
				return "", fmt.Errorf("submitting CSR failed with status %q: %s", response.HTTPResponse.Status, response.JSON400.Message)
			}
			return "", fmt.Errorf("submitting CSR failed with status %q", response.HTTPResponse.Status)
		}
	}
	return "", fmt.Errorf("submitting CSR failed after %d attempts, giving up", maxAttempts)
}

func createCsr(o *CertificateOptions, name string, organization string, priv crypto.PrivateKey) ([]byte, error) {
	days, err := strconv.Atoi(strings.TrimSuffix(o.Expiration, "d"))
	if err != nil {
		return nil, err
	}
	expirationSeconds, err := safecast.ToInt32(days * secondsInDay)
	if err != nil {
		return nil, err
	}

	if name == "" {
		name = uuid.NewString()
	}
	template := &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
		Subject: pkix.Name{
			CommonName: name,
		},
	}

	if organization != "" {
		encoded, err := asn1.Marshal(organization)
		if err != nil {
			return nil, fmt.Errorf("marshalling org ID extension: %w", err)
		}
		template.ExtraExtensions = append(template.ExtraExtensions, pkix.Extension{
			Id:       signer.OIDOrgID,
			Critical: false,
			Value:    encoded,
		})
	}

	csrInner, err := x509.CreateCertificateRequest(rand.Reader, template, priv)
	if err != nil {
		return nil, err
	}

	block := &pem.Block{
		Type:    "CERTIFICATE REQUEST",
		Headers: map[string]string{},
		Bytes:   csrInner,
	}
	csrPEM := pem.EncodeToMemory(block)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(csrInner)))
	base64.StdEncoding.Encode(encoded, csrInner)

	csrResource := &api.CertificateSigningRequest{
		ApiVersion: "v1alpha1",
		Kind:       "CertificateSigningRequest",
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Spec: api.CertificateSigningRequestSpec{
			ExpirationSeconds: &expirationSeconds,
			Request:           csrPEM,
			SignerName:        o.SignerName,
			Usages:            &[]string{"clientAuth", "CA:false"},
		},
	}
	csrResourceJSON, err := json.Marshal(csrResource)
	if err != nil {
		return nil, err
	}

	return csrResourceJSON, nil
}

func getCsr(name string, c *apiclient.ClientWithResponses, ctx context.Context) (*api.CertificateSigningRequest, error) {
	response, err := c.GetCertificateSigningRequestWithResponse(ctx, name)
	if err != nil {
		return nil, err
	}

	if response.HTTPResponse != nil {
		statuscode := response.HTTPResponse.StatusCode
		if statuscode != http.StatusOK && statuscode != http.StatusCreated {
			return nil, fmt.Errorf("%s: reading CertificateSigningRequest: %s", name, string(response.Body))
		}
	}

	currentCsr := api.CertificateSigningRequest{}
	if err := json.Unmarshal([]byte(string(response.Body)), &currentCsr); err != nil {
		return nil, err
	}

	return &currentCsr, nil
}

func createEmbeddedConfig(currentCsr *api.CertificateSigningRequest, priv crypto.PrivateKey, response *apiclient.GetEnrollmentConfigResponse) error {
	pemPriv, err := fccrypto.PEMEncodeKey(priv)
	if err != nil {
		return fmt.Errorf("PEM encoding private key: %w", err)
	}
	cadata, err := base64.StdEncoding.DecodeString(response.JSON200.EnrollmentService.Service.CertificateAuthorityData)
	if err != nil {
		return fmt.Errorf("base64 decoding CA data: %w", err)
	}

	config := lo.ToPtr(config.NewServiceConfig())
	config.EnrollmentService.AuthInfo.ClientCertificateData = *currentCsr.Status.Certificate
	config.EnrollmentService.AuthInfo.ClientKeyData = pemPriv
	config.EnrollmentService.Service.Server = response.JSON200.EnrollmentService.Service.Server
	config.EnrollmentService.Service.CertificateAuthorityData = cadata
	config.EnrollmentService.EnrollmentUIEndpoint = response.JSON200.EnrollmentService.EnrollmentUiEndpoint
	marshalled, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshalling agent service config: %w", err)
	}

	fmt.Print(string(marshalled))
	return nil
}

func createReferenceConfig(name string, response *apiclient.GetEnrollmentConfigResponse) error {
	config := lo.ToPtr(config.NewServiceConfig())
	config.EnrollmentService.AuthInfo.ClientCertificate = filepath.Join(agentPath, name+".crt")
	config.EnrollmentService.AuthInfo.ClientKey = filepath.Join(agentPath, name+".key")
	config.EnrollmentService.Service.Server = response.JSON200.EnrollmentService.Service.Server
	config.EnrollmentService.Service.CertificateAuthority = filepath.Join(agentPath, "ca.crt")
	config.EnrollmentService.EnrollmentUIEndpoint = response.JSON200.EnrollmentService.EnrollmentUiEndpoint
	marshalled, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshalling agent service config: %w", err)
	}

	fmt.Print(string(marshalled))
	return nil
}

func createUniqueName(n string) string {
	u := uuid.NewString()
	return n + "-" + u[:8]
}

func checkCsrCertReady(csr *api.CertificateSigningRequest) bool {
	if csr.Status == nil {
		return false
	} else if csr.Status.Certificate == nil {
		return false
	}
	return true
}

// credit: modified from
// https://github.com/sigstore/cosign/blob/77f71e0d7470e31ed4ed5653fe5a7c8e3b283606/pkg/cosign/common.go#L28
func getPassword() ([]byte, error) {
	fcpass := os.Getenv("FCPASS")
	if fcpass != "" {
		return []byte(fcpass), nil
	}

	fmt.Fprint(os.Stderr, "Enter password for data encryption: ")
	//nolint:unconvert
	pw1, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr)

	fmt.Fprint(os.Stderr, "Enter password for data encryption again: ")
	//nolint:unconvert
	confirmpw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}

	if string(pw1) != string(confirmpw) {
		return nil, fmt.Errorf("passwords do not match")
	}
	return pw1, nil
}
