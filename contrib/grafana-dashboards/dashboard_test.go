package grafanadashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type dashboard struct {
	Panels []panel `json:"panels"`
}

type panel struct {
	FieldConfig struct {
		Defaults struct {
			Unit string `json:"unit"`
		} `json:"defaults"`
		Overrides []panelOverride `json:"overrides"`
	} `json:"fieldConfig"`
	Options struct {
		Legend struct {
			Calcs []string `json:"calcs"`
		} `json:"legend"`
	} `json:"options"`
	Targets []target `json:"targets"`
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	Type    string   `json:"type"`
}

type panelOverride struct {
	Matcher struct {
		ID      string `json:"id"`
		Options string `json:"options"`
	} `json:"matcher"`
	Properties []struct {
		ID    string `json:"id"`
		Value struct {
			FixedColor string `json:"fixedColor"`
		} `json:"value"`
	} `json:"properties"`
}

type target struct {
	Expr         string `json:"expr"`
	LegendFormat string `json:"legendFormat"`
}

func loadDashboard(t *testing.T, name string) dashboard {
	t.Helper()

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	data, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), name))
	if err != nil {
		t.Fatalf("read dashboard %q: %v", name, err)
	}

	var result dashboard
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse dashboard %q: %v", name, err)
	}
	return result
}

func findPanel(t *testing.T, dashboard dashboard, title string) panel {
	t.Helper()
	for _, candidate := range dashboard.Panels {
		if candidate.Title == title {
			return candidate
		}
	}
	t.Fatalf("dashboard panel %q not found", title)
	return panel{}
}

func hasExpressionContaining(targets []target, want string) bool {
	for _, candidate := range targets {
		if strings.Contains(candidate.Expr, want) {
			return true
		}
	}
	return false
}

func hasColorOverride(overrides []panelOverride, matcher, color string) bool {
	for _, override := range overrides {
		if override.Matcher.ID != "byRegexp" || override.Matcher.Options != matcher {
			continue
		}
		for _, property := range override.Properties {
			if property.ID == "color" && property.Value.FixedColor == color {
				return true
			}
		}
	}
	return false
}

func TestFleetDashboardShowsAllFleetStatuses(t *testing.T) {
	dashboard := loadDashboard(t, "flightctl-fleet-dashboard.json")

	t.Run("When update status is rendered it should include every defined update state", func(t *testing.T) {
		update := findPanel(t, dashboard, "Update Status")
		for _, status := range []string{"Unknown", "OutOfDate", "Updating", "UpToDate"} {
			if !hasExpressionContaining(update.Targets, `status="`+status+`"`) {
				t.Errorf("update panel does not query status %q", status)
			}
		}
	})

	t.Run("When application status is rendered it should include devices without applications", func(t *testing.T) {
		applications := findPanel(t, dashboard, "Application Status")
		if !hasExpressionContaining(applications.Targets, `status="NoApplications"`) {
			t.Error("application panel does not query NoApplications")
		}
	})

	t.Run("When device summary is rendered it should expose every summary status", func(t *testing.T) {
		summary := findPanel(t, dashboard, "Devices Summary")
		if len(summary.Targets) != 1 {
			t.Fatalf("expected one summary target, got %d", len(summary.Targets))
		}
		query := summary.Targets[0]
		for _, want := range []string{"flightctl_devices_summary", "sum by (organization_id, status)", `fleet=~"$fleet_selector"`} {
			if !strings.Contains(query.Expr, want) {
				t.Errorf("summary query does not contain %q: %s", want, query.Expr)
			}
		}
		if query.LegendFormat != "{{organization_id}} / {{status}}" {
			t.Errorf("summary legend format = %q, want {{organization_id}} / {{status}}", query.LegendFormat)
		}

		t.Run("When fleet dashboard panels are loaded they should have unique IDs", func(t *testing.T) {
			seen := make(map[int]string, len(dashboard.Panels))
			for _, candidate := range dashboard.Panels {
				if previousTitle, ok := seen[candidate.ID]; ok {
					t.Errorf("panel ID %d is used by %q and %q", candidate.ID, previousTitle, candidate.Title)
				}
				seen[candidate.ID] = candidate.Title
			}
		})
	})
}

func TestAPIDashboardSeparatesPortsAndUsesOperationalUnits(t *testing.T) {
	dashboard := loadDashboard(t, "flightctl-api-dashboard.json")

	for _, testCase := range []struct {
		title string
		port  string
		kind  string
	}{
		{title: "Service API LATENCY", port: "3443", kind: "latency"},
		{title: "Agent API LATENCY", port: "7443", kind: "latency"},
		{title: "Service API TRAFFIC", port: "3443", kind: "traffic"},
		{title: "Agent API TRAFFIC", port: "7443", kind: "traffic"},
		{title: "Service API ERRORS", port: "3443", kind: "errors"},
		{title: "Agent API ERRORS", port: "7443", kind: "errors"},
	} {
		t.Run("When "+testCase.title+" is rendered it should preserve its port dimension", func(t *testing.T) {
			candidate := findPanel(t, dashboard, testCase.title)
			if candidate.Type != "timeseries" {
				t.Fatalf("panel type = %q, want timeseries", candidate.Type)
			}
			wantUnit := "reqps"
			if testCase.kind == "latency" {
				wantUnit = "ms"
			}
			if candidate.FieldConfig.Defaults.Unit != wantUnit {
				t.Errorf("panel unit = %q, want %s", candidate.FieldConfig.Defaults.Unit, wantUnit)
			}
			if !hasExpressionContaining(candidate.Targets, `server_port="`+testCase.port+`"`) {
				t.Errorf("panel has no query for server port %s", testCase.port)
			}

			switch testCase.kind {
			case "latency":
				if len(candidate.Targets) != 3 {
					t.Fatalf("latency target count = %d, want 3", len(candidate.Targets))
				}
				for _, percentile := range []string{"(avg)", "(p50)", "(p99)"} {
					found := false
					for _, target := range candidate.Targets {
						if strings.Contains(target.LegendFormat, percentile) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("latency panel has no %s series", percentile)
					}
				}
				if len(candidate.Options.Legend.Calcs) != 0 {
					t.Errorf("latency legend calculations = %v, want none", candidate.Options.Legend.Calcs)
				}
			case "traffic":
				if !hasExpressionContaining(candidate.Targets, "sum by (http_response_status_code)") {
					t.Error("traffic query is not grouped by response status")
				}
				if !hasExpressionContaining(candidate.Targets, "_count") {
					t.Error("traffic query does not use request count")
				}
			case "errors":
				if !hasExpressionContaining(candidate.Targets, "sum by (http_response_status_code)") {
					t.Error("errors query is not grouped by response status")
				}
				if !hasExpressionContaining(candidate.Targets, `[45]..`) {
					t.Error("errors query does not filter 4xx and 5xx status codes")
				}
				if hasExpressionContaining(candidate.Targets, "increase(") {
					t.Error("errors query reports totals with increase instead of rates")
				}
				for _, color := range []struct {
					matcher string
					color   string
				}{
					{matcher: "^4", color: "yellow"},
					{matcher: "^5", color: "red"},
				} {
					if !hasColorOverride(candidate.FieldConfig.Overrides, color.matcher, color.color) {
						t.Errorf("errors panel has no %s override for %sxx responses", color.color, color.matcher[:2])
					}
				}
			}
		})
	}

	t.Run("When saturation is rendered it should expose peak and database utilization", func(t *testing.T) {
		saturation := findPanel(t, dashboard, "SATURATION")
		if !hasExpressionContaining(saturation.Targets, "gorm_dbstats_open_connections") {
			t.Error("saturation panel does not include open database connections")
		}
		if !hasExpressionContaining(saturation.Targets, "gorm_dbstats_max_open_connections") {
			t.Error("saturation panel does not include maximum database connections")
		}
		foundMax := false
		for _, calculation := range saturation.Options.Legend.Calcs {
			if calculation == "max" {
				foundMax = true
				break
			}
		}
		if !foundMax {
			t.Errorf("saturation legend calculations = %v, want max", saturation.Options.Legend.Calcs)
		}
	})
}
