package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"text/tabwriter"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/thoas/go-funk"
	"sigs.k8s.io/yaml"
)

const (
	yamlFormat = "yaml"
)

var (
	legalOutputTypes = []string{yamlFormat}
)

type GetOptions struct {
	Owner         string
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

			if cmd.Flags().Lookup("owner").Changed {
				if kind != DeviceKind {
					return fmt.Errorf("owner can only be specified when fetching devices")
				}
				if len(name) > 0 {
					return fmt.Errorf("cannot specify owner together with a device name")
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
			var owner *string
			if cmd.Flags().Lookup("owner").Changed {
				owner = &o.Owner
			}
			var limit *int32
			if cmd.Flags().Lookup("limit").Changed {
				limit = &o.Limit
			}
			var cont *string
			if cmd.Flags().Lookup("continue").Changed {
				cont = &o.Continue
			}
			return RunGet(kind, name, labelSelector, owner, o.Output, limit, cont)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVar(&o.Owner, "owner", o.Owner, "filter by owner")
	cmd.Flags().StringVarP(&o.LabelSelector, "labelselector", "l", o.LabelSelector, "label selector as a comma-separated list of key=value")
	cmd.Flags().StringVarP(&o.Output, "output", "o", o.Output, "output format (yaml)")
	cmd.Flags().Int32Var(&o.Limit, "limit", o.Limit, "the maximum number of results returned in the list response")
	cmd.Flags().StringVar(&o.Continue, "continue", o.Continue, "query more results starting from the value of the 'continue' field in the previous response")
	return cmd
}

func RunGet(kind, name string, labelSelector, owner *string, output string, limit *int32, cont *string) error {
	c, err := client.NewFromConfigFile(defaultClientConfigFile)
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	switch kind {
	case DeviceKind:
		if len(name) > 0 {
			response, err := c.ReadDeviceWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading %s/%s: %v", kind, name, err)
			}
			out, err := serializeResponse(response, fmt.Sprintf("%s/%s", kind, name))
			if err != nil {
				return fmt.Errorf("serializing response for %s/%s: %v", kind, name, err)
			}
			fmt.Printf("%s\n", string(out))
		} else {
			params := api.ListDevicesParams{
				Owner:         owner,
				LabelSelector: labelSelector,
				Limit:         limit,
				Continue:      cont,
			}
			response, err := c.ListDevicesWithResponse(context.Background(), &params)
			if err != nil {
				return fmt.Errorf("listing %s: %v", plural(kind), err)
			}
			return printListResourceResponse(response, err, plural(kind), output)
		}
	case EnrollmentRequestKind:
		if len(name) > 0 {
			response, err := c.ReadEnrollmentRequestWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading %s/%s: %v", kind, name, err)
			}
			out, err := serializeResponse(response, fmt.Sprintf("%s/%s", kind, name))
			if err != nil {
				return fmt.Errorf("serializing response for %s/%s: %v", kind, name, err)
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
				return fmt.Errorf("listing %s: %v", plural(kind), err)
			}
			return printListResourceResponse(response, err, plural(kind), output)
		}
	case FleetKind:
		if len(name) > 0 {
			response, err := c.ReadFleetWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading %s/%s: %v", kind, name, err)
			}
			out, err := serializeResponse(response, fmt.Sprintf("%s/%s", kind, name))
			if err != nil {
				return fmt.Errorf("serializing response for %s/%s: %v", kind, name, err)
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
				return fmt.Errorf("listing %s: %v", plural(kind), err)
			}
			return printListResourceResponse(response, err, plural(kind), output)
		}
	case RepositoryKind:
		if len(name) > 0 {
			response, err := c.ReadRepositoryWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading %s/%s: %v", kind, name, err)
			}
			out, err := serializeResponse(response, fmt.Sprintf("%s/%s", kind, name))
			if err != nil {
				return fmt.Errorf("serializing response for %s/%s: %v", kind, name, err)
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
				return fmt.Errorf("listing %s: %v", plural(kind), err)
			}
			return printListResourceResponse(response, err, plural(kind), output)
		}
	case ResourceSyncKind:
		if len(name) > 0 {
			response, err := c.ReadResourceSyncWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading %s/%s: %v", kind, name, err)
			}
			out, err := serializeResponse(response, fmt.Sprintf("%s/%s", kind, name))
			if err != nil {
				return fmt.Errorf("serializing response for %s/%s: %v", kind, name, err)
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
				return fmt.Errorf("listing %s: %v", plural(kind), err)
			}
			return printListResourceResponse(response, err, plural(kind), output)
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}
	return nil
}

func serializeResponse(response interface{}, name string) ([]byte, error) {
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
	case plural(DeviceKind):
		printDevicesTable(w, response.(*apiclient.ListDevicesResponse))
	case plural(EnrollmentRequestKind):
		printEnrollmentRequestsTable(w, response.(*apiclient.ListEnrollmentRequestsResponse))
	case plural(FleetKind):
		printFleetsTable(w, response.(*apiclient.ListFleetsResponse))
	case plural(RepositoryKind):
		printRepositoriesTable(w, response.(*apiclient.ListRepositoriesResponse))
	case plural(ResourceSyncKind):
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
