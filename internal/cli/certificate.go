package cli

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

const secondsInDay = 86400
const agentPath = "certs/"

type CertificateOptions struct {
	GlobalOptions
	Name         string
	Expiration   string
	OutputFormat string
	OutputDir    string
	EncryptKey   bool
	SignerName   string
}

func DefaultCertificateOptions() *CertificateOptions {
	return &CertificateOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Expiration:    "7d",
		OutputFormat:  "reference",
		OutputDir:     "./",
		EncryptKey:    false,
		SignerName:    "enrollment",
	}
}

func NewCmdCertificate() *cobra.Command {
	o := DefaultCertificateOptions()
	cmd := &cobra.Command{
		Use: "certificate request [flags]",
		// more subcommands may be added later
		Short:     "request a new enrollment certificate for a device with 'certificate request'",
		Args:      cobra.MatchAll(cobra.MinimumNArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"request"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *CertificateOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.Name, "name", "n", "csr", "specify a name for the certificate signing request")
	fs.StringVarP(&o.Expiration, "expiration", "x", "7d", "specify desired certificate expiration in days, example: 7d")
	fs.StringVarP(&o.OutputFormat, "output-format", "o", "default", "specify desired output format for an enrollment cert: either 'reference' to have the config file reference key and cert file paths, or 'embedded' to have the key and cert embedded in the config file")
	fs.StringVarP(&o.OutputDir, "output-dir", "d", "./", "specify desired output directory for key, cert, and ca files (defaults to current directory)")
	fs.StringVarP(&o.SignerName, "cert-type", "t", "enrollment", "specify the type of certificate requested: 'enrollment' or 'ca' (default 'enrollment')")
	fs.BoolVarP(&o.EncryptKey, "encrypt", "s", false, "option to encrypt key file with a password from env var $FCPASS, or if $FCPASS is not set password must be provided during runtime")
}

func (o *CertificateOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *CertificateOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	errs := validation.ValidateSignerName(o.SignerName)
	if len(errs) > 0 {
		return fmt.Errorf("invalid certificate type. current certificate types supported: 'enrollment', 'ca'")
	}

	// check if user updated output format while requesting a cert that is not an enrollment cert -
	// output format is only relevant for enrollment certs
	if o.SignerName != "enrollment" && o.OutputFormat != "default" {
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
	log := log.InitLogs()

	name := createUniqueName(o.Name)

	log.Infof("creating new ecdsa key pair...")
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return fmt.Errorf("creating new key pair: %w", err)
	}

	// write out the key asap - in case process is interrupted, key is needed to access
	// status of CSR via enrollmentconfig API
	keypath := o.OutputDir + name + ".key"
	if !o.EncryptKey {
		log.Infof("writing private key to: %s...\n", keypath)
		err = fccrypto.WriteKey(keypath, priv)
		if err != nil {
			return fmt.Errorf("writing private key to %s: %w", keypath, err)
		}
	} else {
		pw, err := getPassword()
		if err != nil {
			return fmt.Errorf("getting password: %w", err)
		}
		log.Infof("writing encrypted private key to: %s...\n", keypath)
		err = fccrypto.WritePasswordEncryptedKey(keypath, priv, pw)
		if err != nil {
			return fmt.Errorf("writing encrypted private key to %s: %w", keypath, err)
		}
	}

	csrResourceJSON, err := createCsr(o, name, priv)
	if err != nil {
		return fmt.Errorf("creating csr resource: %w", err)
	}

	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	log.Infof("submitting CSR to flightctl service...")
	status, err := submitCsr(name, c, ctx, csrResourceJSON)
	if err != nil {
		return fmt.Errorf("submitting csr to flightctl service: %w", err)
	}
	log.Infof("%s: %s\n", status, name)

	// if this isn't an enrollment cert, approval may take arbitrary time, so don't poll for
	// the cert here - we're done!
	if o.SignerName != "enrollment" {
		return nil
	}

	err = wait.PollWithContext(ctx, time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
		log.Infof("checking for certificate...")
		currentCsr, err := getCsr(o, name, c, ctx)
		if err != nil {
			return false, fmt.Errorf("reading csr: %s: %w", ctx.Value("name"), err)
		}
		return checkCsrCertReady(currentCsr), nil
	})
	if err != nil {
		return fmt.Errorf("polling for certificate: %w", err)
	}
	log.Infof("certificate is ready")
	currentCsr, err := getCsr(o, name, c, ctx)
	if err != nil {
		return fmt.Errorf("reading csr %s: %w", name, err)
	}

	// get URIs and other data from enrollmentconfig API
	response, err := c.EnrollmentConfigWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("getting enrollment config for %s: %w", name, err)
	}
	err = validateHttpResponse(response.Body, response.StatusCode(), http.StatusOK)
	if err != nil {
		return fmt.Errorf("getting enrollment config for %s: %w", name, err)
	}

	switch o.OutputFormat {
	case "embedded":
		err = createEmbeddedConfig(name, ctx, priv, response)
	default:
		err = createReferenceConfig(name, currentCsr, priv, response, o.OutputDir)

	}
	if err != nil {
		return fmt.Errorf("creating output as %s: %w:", o.OutputFormat, err)
	}

	return nil
}

func createCsr(o *CertificateOptions, name string, priv crypto.PrivateKey) ([]byte, error) {
	days, err := strconv.Atoi(strings.TrimSuffix(o.Expiration, "d"))
	if err != nil {
		return nil, err
	}
	expirationSeconds := int32(days * secondsInDay)

	// the CN is going to be a FC-generated UUID, populated at signing time
	template := &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
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
			SignerName:        "enrollment",
			Usages:            &[]string{"clientAuth", "CA:false"},
		},
	}
	csrResourceJSON, err := json.Marshal(csrResource)
	if err != nil {
		return nil, err
	}

	return csrResourceJSON, nil
}

func submitCsr(name string, c *apiclient.ClientWithResponses, ctx context.Context, csrResourceJSON []byte) (string, error) {
	var status string

	response, err := c.ReplaceCertificateSigningRequestWithBodyWithResponse(ctx, name, "application/json", bytes.NewReader(csrResourceJSON))
	if err != nil {
		return "", err
	}

	if response.HTTPResponse != nil {
		status = response.HTTPResponse.Status
		statuscode := response.HTTPResponse.StatusCode
		if statuscode != http.StatusOK && statuscode != http.StatusCreated {
			return status, fmt.Errorf("%s: applying CertificateSigningRequest: %s", name, string(response.Body))
		}
	}
	return status, nil
}

func getCsr(o *CertificateOptions, name string, c *apiclient.ClientWithResponses, ctx context.Context) (*api.CertificateSigningRequest, error) {
	response, err := c.ReadCertificateSigningRequestWithResponse(ctx, name)
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

func createEmbeddedConfig(name string, ctx context.Context, priv crypto.PrivateKey, response *apiclient.EnrollmentConfigResponse) error {
	pemPriv, err := fccrypto.PEMEncodeKey(priv)
	if err != nil {
		return err
	}
	response.JSON200.EnrollmentService.Authentication.ClientKeyData = base64.StdEncoding.EncodeToString(pemPriv)

	marshalled, err := yaml.Marshal(response.JSON200)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}

	_, err = fmt.Print(string(marshalled))
	if err != nil {
		return err
	}

	return nil
}

func writeCertFile(data []byte, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating file %s\n", path)
	}
	defer f.Close()
	_, err = f.Write(data)
	if err != nil {
		return fmt.Errorf("writing data to %s: %w\n", path, err)
	}

	return nil
}

func createReferenceConfig(name string, currentCsr *api.CertificateSigningRequest, priv crypto.PrivateKey, response *apiclient.EnrollmentConfigResponse, path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err := os.MkdirAll(path, 0644)
		if err != nil {
			return fmt.Errorf("creating directory %s: %w\n", path, err)
		}
	}

	// write out agent cert
	certpath := path + name + ".crt"
	cert := currentCsr.Status.Certificate
	err := writeCertFile(*cert, certpath)
	if err != nil {
		return err
	}

	// write out ca bundle
	capath := path + "ca.crt"
	cadata, err := base64.StdEncoding.DecodeString(response.JSON200.EnrollmentService.Service.CertificateAuthorityData)
	if err != nil {
		return fmt.Errorf("decoding CA data: %w", err)
	}
	err = writeCertFile(cadata, capath)
	if err != nil {
		return err
	}

	// print config to stdout
	response.JSON200.EnrollmentService.Authentication.ClientKeyData = agentPath + name + ".key"
	response.JSON200.EnrollmentService.Authentication.ClientCertificateData = agentPath + name + ".crt"
	response.JSON200.EnrollmentService.Service.CertificateAuthorityData = agentPath + "ca.crt"

	marshalled, err := yaml.Marshal(response.JSON200)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w\n", err)
	}

	s := string(marshalled)
	// these replacements are necessary only because the enrollmentconfig API struct fields do not 1:1 match
	// the agent.Config and client.Config fields, which maybe should change
	updateCert := strings.Replace(s, "client-certificate-data", "client-certificate", -1)
	updateKey := strings.Replace(updateCert, "client-key-data", "client-key", -1)
	stringOut := strings.Replace(updateKey, "certificate-authority-data", "certificate-authority", -1)

	_, err = fmt.Print(stringOut)
	if err != nil {
		return err
	}

	return nil
}

func createUniqueName(n string) string {
	if n == "csr" {
		u := uuid.NewString()
		n = n + "-" + u[:8]
	}
	return n
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
	// nolint:unconvert
	pw1, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr)

	fmt.Fprint(os.Stderr, "Enter password for data encryption again: ")
	// nolint:unconvert
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
