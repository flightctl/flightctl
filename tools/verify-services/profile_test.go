package main

import (
	"testing"
)

func TestDiffSets_WhenEqual_itShouldBeEmpty(t *testing.T) {
	want := toSet([]string{"a", "b"})
	got := toSet([]string{"b", "a"})
	d := DiffSets(want, got)
	if !d.Empty() {
		t.Fatalf("expected empty diff, got %+v", d)
	}
}

func TestDiffSets_WhenMissingAndUnexpected_itShouldReportBoth(t *testing.T) {
	want := toSet([]string{"a", "b"})
	got := toSet([]string{"b", "c"})
	d := DiffSets(want, got)
	if len(d.Missing) != 1 || d.Missing[0] != "a" {
		t.Fatalf("missing: got %v", d.Missing)
	}
	if len(d.Unexpected) != 1 || d.Unexpected[0] != "c" {
		t.Fatalf("unexpected: got %v", d.Unexpected)
	}
}

func TestToCamelCase_WhenHyphenated_itShouldCamelCase(t *testing.T) {
	cases := map[string]string{
		"api":                "api",
		"remote-access":      "remoteAccess",
		"alertmanager-proxy": "alertmanagerProxy",
		"imagebuilder-api":   "imagebuilderApi",
		"telemetry-gateway":  "telemetryGateway",
	}
	for in, want := range cases {
		if got := toCamelCase(in); got != want {
			t.Errorf("toCamelCase(%q)=%q want %q", in, got, want)
		}
	}
}

func TestExpandService_WhenBackendExternal_itShouldRequireRouteNginxTLS(t *testing.T) {
	exp, err := expandService(serviceEntry{Name: "remote-access", Profile: profileBackendExternal})
	if err != nil {
		t.Fatal(err)
	}
	if !exp.RequireRoute || !exp.RequireNginx || !exp.NeedsTLS || exp.HelmNamespace != "external" {
		t.Fatalf("unexpected expansion: %+v", exp)
	}
	if exp.HelmValuesKey != "remoteAccess" {
		t.Fatalf("helmValuesKey=%q", exp.HelmValuesKey)
	}
	if exp.CertSanFlag != "remote-access" {
		t.Fatalf("certSanFlag=%q", exp.CertSanFlag)
	}
}

func TestExpandService_WhenBackendInternal_itShouldNotRequireRoute(t *testing.T) {
	exp, err := expandService(serviceEntry{Name: "worker", Profile: profileBackendInternal})
	if err != nil {
		t.Fatal(err)
	}
	if exp.RequireRoute || exp.RequireNginx || exp.NeedsTLS {
		t.Fatalf("internal should not require route/nginx/tls: %+v", exp)
	}
	if exp.RequireServiceAccount {
		t.Fatalf("ServiceAccount must be opt-in, not profile default: %+v", exp)
	}
	if exp.HelmNamespace != "internal" {
		t.Fatalf("unexpected: %+v", exp)
	}
}

func TestExpandService_WhenOverrides_itShouldApply(t *testing.T) {
	quadlet := true
	flag := "telemetry"
	key := "imageBuilderApi"
	exp, err := expandService(serviceEntry{
		Name:          "telemetry-gateway",
		Profile:       profileBackendExternal,
		CertSanFlag:   &flag,
		HelmValuesKey: &key,
		Quadlet:       &quadlet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp.CertSanFlag != "telemetry" {
		t.Fatalf("certSanFlag=%q", exp.CertSanFlag)
	}
	if exp.HelmValuesKey != "imageBuilderApi" {
		t.Fatalf("helmValuesKey=%q", exp.HelmValuesKey)
	}
}

func TestExpandService_WhenUnknownProfile_itShouldError(t *testing.T) {
	_, err := expandService(serviceEntry{Name: "x", Profile: "nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}
