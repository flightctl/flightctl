package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"text/tabwriter"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/thoas/go-funk"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"
)

const (
	yamlFormat = "yaml"
)

var (
	clientConfigFile string
	resourceKinds    = map[string]string{
		"device":            "",
		"enrollmentrequest": "",
		"fleet":             "",
		"repository":        "",
		"resourcesync":      "",
	}
	resourceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9\-]+$`)
	fileExtensions    = []string{".json", ".yaml", ".yml"}
	inputExtensions   = append(fileExtensions, "stdin")
	legalOutputTypes  = []string{yamlFormat}
)

func init() {
	clientConfigFile = filepath.Join(homedir.HomeDir(), ".flightctl", "client.yaml")
}

func main() {
	command := NewFlightctlCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func parseAndValidateKindName(arg string) (string, string, error) {
	kind, name, _ := strings.Cut(arg, "/")
	kind = singular(kind)
	if _, ok := resourceKinds[kind]; !ok {
		return "", "", fmt.Errorf("invalid resource kind: %s", kind)
	}
	if len(name) > 0 && !resourceNameRegex.MatchString(name) {
		return "", "", fmt.Errorf("invalid resource name: %s", name)
	}
	return kind, name, nil
}

func singular(kind string) string {
	if kind == "repositories" {
		return "repository"
	} else if strings.HasSuffix(kind, "s") {
		return kind[:len(kind)-1]
	}
	return kind
}

func NewFlightctlCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl",
		Short: "flightctl controls the Flight Control device management service",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(NewCmdGet())
	cmd.AddCommand(NewCmdApply())
	cmd.AddCommand(NewCmdDelete())
	cmd.AddCommand(NewCmdApprove())
	return cmd
}

type GetOptions struct {
	FleetName     string
	LabelSelector string
	Output        string
	Limit         int32
	Continue      string
}

func NewCmdGet() *cobra.Command {
	o := &GetOptions{LabelSelector: "", Limit: 0, Continue: ""}

	cmd := &cobra.Command{
		Use:   "get",
		Short: "get resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseAndValidateKindName(args[0])
			if err != nil {
				return err
			}
			if len(name) > 0 && cmd.Flags().Lookup("labelselector").Changed {
				return fmt.Errorf("cannot specify label selector together when fetching a single resource")
			}

			if cmd.Flags().Lookup("fleetname").Changed {
				if kind != "device" {
					return fmt.Errorf("fleetname can only be specified when fetching devices")
				}
				if len(name) > 0 {
					return fmt.Errorf("cannot specify fleetname together with a device name")
				}
			}

			if cmd.Flags().Lookup("output").Changed && !funk.Contains(legalOutputTypes, o.Output) {
				return fmt.Errorf("output format must be one of %s", strings.Join(legalOutputTypes, ", "))
			}
			if o.Limit < 0 {
				return fmt.Errorf("limit must be greater than 0")
			}
			var labelSelector *string
			if cmd.Flags().Lookup("labelselector").Changed {
				labelSelector = &o.LabelSelector
			}
			var fleetName *string
			if cmd.Flags().Lookup("fleetname").Changed {
				fleetName = &o.FleetName
			}
			var limit *int32
			if cmd.Flags().Lookup("limit").Changed {
				limit = &o.Limit
			}
			var cont *string
			if cmd.Flags().Lookup("continue").Changed {
				cont = &o.Continue
			}
			return RunGet(kind, name, labelSelector, fleetName, o.Output, limit, cont)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&o.FleetName, "fleetname", "f", o.FleetName, "fleet name selector for listing devices")
	cmd.Flags().StringVarP(&o.LabelSelector, "labelselector", "l", o.LabelSelector, "label selector as a comma-separated list of key=value")
	cmd.Flags().StringVarP(&o.Output, "output", "o", o.Output, "output format (yaml)")
	cmd.Flags().Int32Var(&o.Limit, "limit", o.Limit, "the maximum number of results returned in the list response")
	cmd.Flags().StringVar(&o.Continue, "continue", o.Continue, "query more results starting from the value of the 'continue' field in the previous response")
	return cmd
}

type ApplyOptions struct {
	Filenames []string
	DryRun    bool
	Recursive bool
}

func NewCmdApply() *cobra.Command {
	o := &ApplyOptions{Filenames: []string{}, DryRun: false, Recursive: false}

	cmd := &cobra.Command{
		Use:                   "apply -f FILENAME",
		DisableFlagsInUseLine: true,
		Short:                 "apply a configuration to a resource by file name or stdin",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Lookup("filename").Changed {
				return fmt.Errorf("must specify -f FILENAME")
			}
			if len(args) > 0 {
				return fmt.Errorf("unexpected arguments: %v (did you forget to quote wildcards?)", args)
			}
			return RunApply(o.Filenames, o.Recursive, o.DryRun)
		},
		SilenceUsage: true,
	}

	flags := cmd.Flags()
	flags.StringSliceVarP(&o.Filenames, "filename", "f", o.Filenames, "read resources from file or directory")
	annotations := make([]string, 0, len(fileExtensions))
	for _, ext := range fileExtensions {
		annotations = append(annotations, strings.TrimLeft(ext, "."))
	}
	err := flags.SetAnnotation("filename", cobra.BashCompFilenameExt, annotations)
	if err != nil {
		log.Fatalf("setting filename flag annotation: %v", err)
	}
	flags.BoolVarP(&o.DryRun, "dry-run", "", o.DryRun, "only print the object that would be sent, without sending it")
	flags.BoolVarP(&o.Recursive, "recursive", "R", o.Recursive, "process the directory used in -f, --filename recursively")

	return cmd
}

func NewCmdDelete() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "delete resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseAndValidateKindName(args[0])
			if err != nil {
				return err
			}
			return RunDelete(kind, name)
		},
		SilenceUsage: true,
	}
	return cmd
}

func RunGet(kind, name string, labelSelector, fleetName *string, output string, limit *int32, cont *string) error {
	c, err := client.NewFromConfigFile(clientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	switch kind {
	case "device":
		if len(name) > 0 {
			response, err := c.ReadDeviceWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading device/%s: %v", name, err)
			}
			out, err := serializeResponse(response, err, fmt.Sprintf("device/%s", name))
			if err != nil {
				return fmt.Errorf("serializing response for device/%s: %v", name, err)
			}
			fmt.Printf("%s\n", string(out))
		} else {
			params := api.ListDevicesParams{
				FleetName:     fleetName,
				LabelSelector: labelSelector,
				Limit:         limit,
				Continue:      cont,
			}
			response, err := c.ListDevicesWithResponse(context.Background(), &params)
			if err != nil {
				return fmt.Errorf("listing devices: %v", err)
			}
			return printListResourceResponse(response, err, "devices", output)
		}
	case "enrollmentrequest":
		if len(name) > 0 {
			response, err := c.ReadEnrollmentRequestWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading enrollmentrequest/%s: %v", name, err)
			}
			out, err := serializeResponse(response, err, fmt.Sprintf("enrollmentrequest/%s", name))
			if err != nil {
				return fmt.Errorf("serializing response for enrollmentrequest/%s: %v", name, err)
			}
			fmt.Printf("%s\n", string(out))
		} else {
			params := api.ListEnrollmentRequestsParams{
				LabelSelector: labelSelector,
				Limit:         limit,
				Continue:      cont,
			}
			response, err := c.ListEnrollmentRequestsWithResponse(context.Background(), &params)
			if err != nil {
				return fmt.Errorf("listing enrollmentrequests: %v", err)
			}
			return printListResourceResponse(response, err, "enrollmentrequests", output)
		}
	case "fleet":
		if len(name) > 0 {
			response, err := c.ReadFleetWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading fleet/%s: %v", name, err)
			}
			out, err := serializeResponse(response, err, fmt.Sprintf("fleet/%s", name))
			if err != nil {
				return fmt.Errorf("serializing response for fleet/%s: %v", name, err)
			}
			fmt.Printf("%s\n", string(out))
		} else {
			params := api.ListFleetsParams{
				LabelSelector: labelSelector,
				Limit:         limit,
				Continue:      cont,
			}

			response, err := c.ListFleetsWithResponse(context.Background(), &params)
			if err != nil {
				return fmt.Errorf("listing fleets: %v", err)
			}
			return printListResourceResponse(response, err, "fleets", output)
		}
	case "repository":
		if len(name) > 0 {
			response, err := c.ReadRepositoryWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading repository/%s: %v", name, err)
			}
			out, err := serializeResponse(response, err, fmt.Sprintf("repository/%s", name))
			if err != nil {
				return fmt.Errorf("serializing response for repository/%s: %v", name, err)
			}
			fmt.Printf("%s\n", string(out))
		} else {
			params := api.ListRepositoriesParams{
				LabelSelector: labelSelector,
				Limit:         limit,
				Continue:      cont,
			}

			response, err := c.ListRepositoriesWithResponse(context.Background(), &params)
			if err != nil {
				return fmt.Errorf("listing repositories: %v", err)
			}
			return printListResourceResponse(response, err, "repositories", output)
		}
	case "resourcesync":
		if len(name) > 0 {
			response, err := c.ReadResourceSyncWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading resourcesync/%s: %v", name, err)
			}
			out, err := serializeResponse(response, err, fmt.Sprintf("resourcesync/%s", name))
			if err != nil {
				return fmt.Errorf("serializing response for resourcesync/%s: %v", name, err)
			}
			fmt.Printf("%s\n", string(out))
		} else {
			params := api.ListResourceSyncParams{
				LabelSelector: labelSelector,
				Limit:         limit,
				Continue:      cont,
			}

			response, err := c.ListResourceSyncWithResponse(context.Background(), &params)
			if err != nil {
				return fmt.Errorf("listing resourcesyncs: %v", err)
			}
			return printListResourceResponse(response, err, "resourcesyncs", output)
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}
	return nil
}

func serializeResponse(response interface{}, err error, name string) ([]byte, error) {
	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int() != http.StatusOK {
		return nil, fmt.Errorf("reading %s: %d", name, v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int())
	}

	return yaml.Marshal(v.FieldByName("JSON200").Interface())
}

func printListResourceResponse(response interface{}, err error, resourceType string, output string) error {
	if err != nil {
		return fmt.Errorf("listing %s: %v", resourceType, err)
	}
	v := reflect.ValueOf(response).Elem()
	if v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int() != http.StatusOK {
		return fmt.Errorf("listing %s: %d", resourceType, v.FieldByName("HTTPResponse").Elem().FieldByName("StatusCode").Int())
	}

	if output == yamlFormat {
		marshalled, err := yaml.Marshal(v.FieldByName("JSON200").Interface())
		if err != nil {
			return fmt.Errorf("marshalling resource: %v", err)
		}

		fmt.Printf("%s\n", string(marshalled))
		return nil
	}

	// Tabular
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)
	switch resourceType {
	case "devices":
		printDevicesTable(w, response.(*apiclient.ListDevicesResponse))
	case "enrollmentrequests":
		printEnrollmentRequestsTable(w, response.(*apiclient.ListEnrollmentRequestsResponse))
	case "fleets":
		printFleetsTable(w, response.(*apiclient.ListFleetsResponse))
	case "repositories":
		printRepositoriesTable(w, response.(*apiclient.ListRepositoriesResponse))
	case "resourcesyncs":
		printResourceSyncsTable(w, response.(*apiclient.ListResourceSyncResponse))
	default:
		return fmt.Errorf("unknown resource type %s", resourceType)
	}
	w.Flush()
	return nil
}

func printDevicesTable(w *tabwriter.Writer, response *apiclient.ListDevicesResponse) {
	fmt.Fprintln(w, "NAME")
	for _, d := range response.JSON200.Items {
		fmt.Fprintln(w, *d.Metadata.Name)
	}
}

func printEnrollmentRequestsTable(w *tabwriter.Writer, response *apiclient.ListEnrollmentRequestsResponse) {
	fmt.Fprintln(w, "NAME\tAPPROVED\tREGION")
	for _, e := range response.JSON200.Items {
		approved := ""
		region := ""
		if e.Status.Approval != nil {
			approved = fmt.Sprintf("%t", e.Status.Approval.Approved)
			region = *e.Status.Approval.Region
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", *e.Metadata.Name, approved, region)
	}
}

func printFleetsTable(w *tabwriter.Writer, response *apiclient.ListFleetsResponse) {
	fmt.Fprintln(w, "NAME")
	for _, f := range response.JSON200.Items {
		fmt.Fprintln(w, *f.Metadata.Name)
	}
}

func printRepositoriesTable(w *tabwriter.Writer, response *apiclient.ListRepositoriesResponse) {
	fmt.Fprintln(w, "NAME\tACCESSIBLE\tREASON\tMESSAGE")

	for _, f := range response.JSON200.Items {
		accessible := "-"
		reason := ""
		message := ""
		if f.Status != nil && f.Status.Conditions != nil && len(*f.Status.Conditions) > 0 {
			accessible = string((*f.Status.Conditions)[0].Status)
			if (*f.Status.Conditions)[0].Reason != nil {
				reason = *(*f.Status.Conditions)[0].Reason
			}
			if (*f.Status.Conditions)[0].Message != nil {
				message = *(*f.Status.Conditions)[0].Message
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", *f.Metadata.Name, accessible, reason, message)
	}
}

func printResourceSyncsTable(w *tabwriter.Writer, response *apiclient.ListResourceSyncResponse) {
	fmt.Fprintln(w, "NAME\tREPOSITORY\tPATH")

	for _, f := range response.JSON200.Items {
		reponame := *f.Spec.Repository
		path := *f.Spec.Path
		fmt.Fprintf(w, "%s\t%s\t%s\n", *f.Metadata.Name, reponame, path)
	}
}

func expandIfFilePattern(pattern string) ([]string, error) {
	if _, err := os.Stat(pattern); os.IsNotExist(err) {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) == 0 {
			return nil, fmt.Errorf("the path %q does not exist", pattern)
		}
		if err == filepath.ErrBadPattern {
			return nil, fmt.Errorf("pattern %q is not valid: %v", pattern, err)
		}
		return matches, err
	}
	return []string{pattern}, nil
}

func ignoreFile(path string, extensions []string) bool {
	if len(extensions) == 0 {
		return false
	}
	ext := filepath.Ext(path)
	for _, s := range extensions {
		if s == ext {
			return false
		}
	}
	return true
}

type genericResource map[string]interface{}

func applyFromReader(client *apiclient.ClientWithResponses, filename string, r io.Reader, dryRun bool) []error {
	decoder := yamlutil.NewYAMLOrJSONDecoder(r, 100)
	resources := []genericResource{}

	var err error
	for {
		var resource genericResource
		err = decoder.Decode(&resource)
		if err != nil {
			break
		}
		resources = append(resources, resource)
	}
	if !errors.Is(err, io.EOF) {
		return []error{err}
	}

	errs := make([]error, 0)
	for _, resource := range resources {
		kind, ok := resource["kind"].(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: skipping resource of unspecified kind: %v", filename, resource))
			continue
		}
		metadata, ok := resource["metadata"].(map[string]interface{})
		if !ok {
			errs = append(errs, fmt.Errorf("%s: skipping resource of unspecified metadata: %v", filename, resource))
			continue
		}
		resourceName, ok := metadata["name"].(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: skipping resource of unspecified resource name: %v", filename, resource))
			continue
		}

		if dryRun {
			fmt.Printf("%s: applying device/%s (dry run only)\n", filename, resourceName)
			continue
		}
		fmt.Printf("%s: applying device/%s: ", filename, resourceName)
		buf, err := json.Marshal(resource)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: skipping resource of kind %q: %v", filename, kind, err))
		}

		switch kind {
		case "Device":
			response, err := client.ReplaceDeviceWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("%s\n", response.HTTPResponse.Status)

		case "EnrollmentRequest":
			response, err := client.ReplaceEnrollmentRequestWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("%s\n", response.HTTPResponse.Status)

		case "Fleet":
			response, err := client.ReplaceFleetWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("%s\n", response.HTTPResponse.Status)
		case "Repository":
			response, err := client.ReplaceRepositoryWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("%s\n", response.HTTPResponse.Status)
		case "ResourceSync":
			response, err := client.ReplaceResourceSyncWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("%s\n", response.HTTPResponse.Status)
		default:
			errs = append(errs, fmt.Errorf("%s: skipping resource of unkown kind %q: %v", filename, kind, resource))
		}
	}
	return errs
}

func RunApply(filenames []string, recursive bool, dryRun bool) error {
	c, err := client.NewFromConfigFile(clientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	errs := make([]error, 0)
	for _, filename := range filenames {
		switch {
		case filename == "-":
			errs = append(errs, applyFromReader(c, "<stdin>", os.Stdin, dryRun)...)
		default:
			expandedFilenames, err := expandIfFilePattern(filename)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, filename := range expandedFilenames {
				_, err := os.Stat(filename)
				if os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("the path %q does not exist", filename))
					continue
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("the path %q cannot be accessed: %v", filename, err))
					continue
				}
				err = filepath.Walk(filename, func(path string, fi os.FileInfo, err error) error {
					if err != nil {
						return err
					}

					if fi.IsDir() {
						if path != filename && !recursive {
							return filepath.SkipDir
						}
						return nil
					}
					// Don't check extension if the filepath was passed explicitly
					if path != filename && ignoreFile(path, inputExtensions) {
						return nil
					}

					r, err := os.Open(path)
					if err != nil {
						return nil
					}
					defer r.Close()
					errs = append(errs, applyFromReader(c, path, r, dryRun)...)
					return nil
				})
				if err != nil {
					errs = append(errs, fmt.Errorf("error walking %q: %v", filename, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func RunDelete(kind, name string) error {
	c, err := client.NewFromConfigFile(clientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	switch kind {
	case "device":
		if len(name) > 0 {
			response, err := c.DeleteDeviceWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting device/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteDevicesWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting devices: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "enrollmentrequest":
		if len(name) > 0 {
			response, err := c.DeleteEnrollmentRequestWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting enrollmentrequest/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteEnrollmentRequestsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting enrollmentrequests: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "fleet":
		if len(name) > 0 {
			response, err := c.DeleteFleetWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting fleet/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteFleetsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting fleets: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "repository":
		if len(name) > 0 {
			response, err := c.DeleteRepositoryWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting repository/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteRepositoriesWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting repositories: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "resourcesync":
		if len(name) > 0 {
			response, err := c.DeleteResourceSyncWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting repository/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteResourceSyncsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting repositories: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return nil
}
