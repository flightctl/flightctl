package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	api "github.com/flightctl/flightctl/api/v1alpha1"
)

const (
	NoneString = "<none>"
)

const (
	DeviceKind                    = "device"
	EnrollmentRequestKind         = "enrollmentrequest"
	FleetKind                     = "fleet"
	RepositoryKind                = "repository"
	ResourceSyncKind              = "resourcesync"
	TemplateVersionKind           = "templateversion"
	CertificateSigningRequestKind = "certificatesigningrequest"
)

var (
	pluralKinds = map[string]string{
		DeviceKind:                    "devices",
		EnrollmentRequestKind:         "enrollmentrequests",
		FleetKind:                     "fleets",
		RepositoryKind:                "repositories",
		ResourceSyncKind:              "resourcesyncs",
		TemplateVersionKind:           "templateversions",
		CertificateSigningRequestKind: "certificatesigningrequests",
	}

	shortnameKinds = map[string]string{
		DeviceKind:                    "dev",
		EnrollmentRequestKind:         "er",
		FleetKind:                     "flt",
		RepositoryKind:                "repo",
		ResourceSyncKind:              "rs",
		TemplateVersionKind:           "tv",
		CertificateSigningRequestKind: "csr",
	}
)

func getValidResourceKinds() []string {
	resourceKinds := make([]string, len(pluralKinds))
	i := 0
	for _, v := range pluralKinds {
		resourceKinds[i] = v
		i++
	}
	return resourceKinds
}

func parseAndValidateKindName(arg string) (string, string, error) {
	kind, name, _ := strings.Cut(arg, "/")
	kind = singular(kind)
	kind = fullname(kind)
	if _, ok := pluralKinds[kind]; !ok {
		return "", "", fmt.Errorf("invalid resource kind: %s", kind)
	}
	return kind, name, nil
}

func singular(kind string) string {
	for singular, plural := range pluralKinds {
		if kind == plural {
			return singular
		}
	}
	return kind
}

func plural(kind string) string {
	return pluralKinds[kind]
}

func fullname(kind string) string {
	for fullname, shortname := range shortnameKinds {
		if kind == shortname {
			return fullname
		}
	}
	return kind
}

// This signature must match the OpenAPI RequestEditorFn type
func nopEditor(ctx context.Context, req *http.Request) error {
	return nil
}

func printRawHttpRequest(ctx context.Context, req *http.Request) error {
	fmt.Printf("===REQUEST BEGIN===\n")
	fmt.Printf("%s %s %s\n", req.Method, req.URL.Path, req.Proto)
	for k, v := range req.Header {
		fmt.Printf("%s: %s\n", k, v)
	}

	var delim string

	if req.Body != nil {
		reader, err := req.GetBody()
		if err != nil {
			return fmt.Errorf("could not get request body")
		}

		buf, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("could not read raw HTTP body")
		}

		body := string(buf)
		fmt.Printf("\n%s", body)
		if body[len(body)-1] == '\n' {
			delim = ""
		} else {
			delim = "\n"
		}
	} else {
		delim = "\n"
	}

	fmt.Printf("%s===REQUEST END===\n", delim)
	return nil
}

// We must ask separately for the body because the OpenAPI client library will have already consumed the
// http.Response.Body field by the time this function is called
func printRawHttpResponse(resp *http.Response, bodyBytes []byte) {
	body := string(bodyBytes)

	fmt.Printf("===RESPONSE BEGIN===\n")
	fmt.Printf("%s %s\n", resp.Proto, resp.Status)
	for k, v := range resp.Header {
		fmt.Printf("%s: %s\n", k, v)
	}

	var delim string
	fmt.Printf("\n%s", body)

	if body[len(body)-1] == '\n' {
		delim = ""
	} else {
		delim = "\n"
	}

	fmt.Printf("%s===RESPONSE END===\n", delim)
}

func getPrintHttpFn(o *GlobalOptions) apiclient.RequestEditorFn {
	if o.VerboseHttp {
		return printRawHttpRequest
	}
	return nopEditor
}

func reflectResponse(response any) (*http.Response, []byte) {
	v := reflect.ValueOf(response).Elem()
	httpResponse := v.FieldByName("HTTPResponse").Interface().(*http.Response)
	body := v.FieldByName("Body").Interface().([]byte)

	return httpResponse, body
}

func validateHttpResponse(responseBody []byte, statusCode int, expectedStatusCode int) error {
	if statusCode != expectedStatusCode {
		var responseError api.Error
		err := json.Unmarshal(responseBody, &responseError)
		if err != nil {
			return err
		}
		return fmt.Errorf("%d %s", statusCode, responseError.Message)
	}
	return nil
}
