package main

import (
	"testing"
)

func TestDiffSets(t *testing.T) {
	cases := []struct {
		name       string
		want, got  []string
		missing    []string
		unexpected []string
	}{
		{
			name: "When sets are equal it should be empty",
			want: []string{"a", "b"},
			got:  []string{"b", "a"},
		},
		{
			name:       "When missing and unexpected it should report both",
			want:       []string{"a", "b"},
			got:        []string{"b", "c"},
			missing:    []string{"a"},
			unexpected: []string{"c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DiffSets(toSet(tc.want), toSet(tc.got))
			if len(d.Missing) != len(tc.missing) {
				t.Fatalf("missing: got %v want %v", d.Missing, tc.missing)
			}
			for i, m := range tc.missing {
				if d.Missing[i] != m {
					t.Fatalf("missing[%d]=%q want %q", i, d.Missing[i], m)
				}
			}
			if len(d.Unexpected) != len(tc.unexpected) {
				t.Fatalf("unexpected: got %v want %v", d.Unexpected, tc.unexpected)
			}
			for i, u := range tc.unexpected {
				if d.Unexpected[i] != u {
					t.Fatalf("unexpected[%d]=%q want %q", i, d.Unexpected[i], u)
				}
			}
		})
	}
}

func TestToCamelCase(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "When single word it should leave unchanged", in: "api", want: "api"},
		{name: "When hyphenated it should camelCase", in: "remote-access", want: "remoteAccess"},
		{name: "When alertmanager-proxy it should camelCase", in: "alertmanager-proxy", want: "alertmanagerProxy"},
		{name: "When imagebuilder-api it should camelCase", in: "imagebuilder-api", want: "imagebuilderApi"},
		{name: "When telemetry-gateway it should camelCase", in: "telemetry-gateway", want: "telemetryGateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toCamelCase(tc.in); got != tc.want {
				t.Fatalf("toCamelCase(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandService(t *testing.T) {
	quadlet := true
	flag := "telemetry"
	key := "imageBuilderApi"

	cases := []struct {
		name    string
		entry   serviceEntry
		wantErr bool
		check   func(t *testing.T, exp ExpandedService)
	}{
		{
			name:  "When backend-external it should require route nginx TLS",
			entry: serviceEntry{Name: "remote-access", Profile: profileBackendExternal},
			check: func(t *testing.T, exp ExpandedService) {
				t.Helper()
				if !exp.RequireRoute || !exp.RequireGateway || !exp.NeedsTLS || exp.HelmNamespace != "external" {
					t.Fatalf("unexpected expansion: %+v", exp)
				}
				if exp.HelmValuesKey != "remoteAccess" {
					t.Fatalf("helmValuesKey=%q", exp.HelmValuesKey)
				}
				if exp.CertSanFlag != "remote-access" {
					t.Fatalf("certSanFlag=%q", exp.CertSanFlag)
				}
			},
		},
		{
			name:  "When backend-internal it should not require route or ServiceAccount",
			entry: serviceEntry{Name: "worker", Profile: profileBackendInternal},
			check: func(t *testing.T, exp ExpandedService) {
				t.Helper()
				if exp.RequireRoute || exp.RequireGateway || exp.NeedsTLS {
					t.Fatalf("internal should not require route/nginx/tls: %+v", exp)
				}
				if exp.RequireServiceAccount {
					t.Fatalf("ServiceAccount must be opt-in, not profile default: %+v", exp)
				}
				if exp.HelmNamespace != "internal" {
					t.Fatalf("unexpected: %+v", exp)
				}
			},
		},
		{
			name: "When overrides are set it should apply them",
			entry: serviceEntry{
				Name:          "telemetry-gateway",
				Profile:       profileBackendExternal,
				CertSanFlag:   &flag,
				HelmValuesKey: &key,
				Quadlet:       &quadlet,
			},
			check: func(t *testing.T, exp ExpandedService) {
				t.Helper()
				if exp.CertSanFlag != "telemetry" {
					t.Fatalf("certSanFlag=%q", exp.CertSanFlag)
				}
				if exp.HelmValuesKey != "imageBuilderApi" {
					t.Fatalf("helmValuesKey=%q", exp.HelmValuesKey)
				}
			},
		},
		{
			name:    "When unknown profile it should error",
			entry:   serviceEntry{Name: "x", Profile: "nope"},
			wantErr: true,
		},
		{
			name:    "When name has path traversal it should error",
			entry:   serviceEntry{Name: "../etc", Profile: profileBackendInternal},
			wantErr: true,
		},
		{
			name: "When helmDir has slash it should error",
			entry: serviceEntry{
				Name:    "worker",
				Profile: profileBackendInternal,
				HelmDir: strPtr("../escape"),
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp, err := expandService(tc.entry)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, exp)
		})
	}
}

func strPtr(s string) *string { return &s }
