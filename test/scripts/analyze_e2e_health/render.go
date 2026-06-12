package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"strings"
)

//go:embed template.html
var htmlReportTemplate string

// renderContext wraps the computed reportData and adds pre-generated SVG
// charts. The HTML template receives a *renderContext so it can access both
// the plain data fields (promoted from the embedded *reportData) and the
// chart SVGs in a single pass.
type renderContext struct {
	*reportData
	PipelineChartSVG template.HTML
	FlakeDonutSVG    template.HTML
	SlowestChartSVG  template.HTML
}

type slackSummary struct {
	// Pipeline timing (mirrors overview row 1)
	WallTimeSecs    float64 `json:"wall_time_seconds"`
	PrepareTimeSecs float64 `json:"prepare_time_seconds"`
	TestingTimeSecs float64 `json:"testing_time_seconds"`
	InferredShards  int     `json:"inferred_shards"`
	AnalyzedRuns    int     `json:"analyzed_runs"`
	// Test health (mirrors overview row 2)
	TotalSpecsTracked   int `json:"total_specs_tracked"`
	ConsistentlyFailing int `json:"consistently_failing"`
	FlakyTests          int `json:"flaky_tests"`
	NeverFailed         int `json:"never_failed"`
	// Extra context
	InfraInstabilityEvents  int     `json:"infra_instability_events"`
	OptimizationSavingsSecs float64 `json:"optimization_savings_seconds"`
}

// renderHTML generates all SVG charts, builds a renderContext, and executes
// the embedded HTML template, writing the result to path.
func renderHTML(data *reportData, path string) error {
	ctx := &renderContext{
		reportData:       data,
		PipelineChartSVG: svgPipelineBars(data.PipelinePhases),
		FlakeDonutSVG: svgDonut([]donutSegment{
			{Label: "Clean", Value: float64(data.CleanCount), Color: "#22c55e"},
			{Label: "Flaky", Value: float64(data.FlakyCount), Color: "#f59e0b"},
			{Label: "Consistently failing", Value: float64(data.ConsistentlyFailingCount), Color: "#ef4444"},
		}, 260),
		SlowestChartSVG: svgSpecBars(data.SlowestTests, "#f59e0b"),
	}

	funcMap := template.FuncMap{
		"FormatDuration": formatDuration,
		"FailPct":        func(rate float64) string { return fmt.Sprintf("%.0f%%", rate*100) },
		"Abs":            math.Abs,
		"Sub":            func(a, b float64) float64 { return a - b },
		"IntSub":         func(a, b int) int { return a - b },
	}
	tmpl, err := template.New("report").Funcs(funcMap).Parse(htmlReportTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()
	return tmpl.Execute(f, ctx)
}

func renderSlackSummary(r *reportData, path string) error {
	s := slackSummary{
		WallTimeSecs:            r.EstimatedWallTimeSecs,
		PrepareTimeSecs:         r.PipelineOverheadSecs,
		TestingTimeSecs:         r.BaselineShardSecs,
		InferredShards:          r.InferredShardCount,
		AnalyzedRuns:            r.AnalyzedRuns,
		TotalSpecsTracked:       r.TotalAnalyzedSpecs,
		ConsistentlyFailing:     r.ConsistentlyFailingCount,
		FlakyTests:              r.FlakyCount,
		NeverFailed:             r.CleanCount,
		InfraInstabilityEvents:  r.InfraInstabilityCount,
		OptimizationSavingsSecs: r.OptimBaselineWorkflowSecs - r.OptimBestWorkflowSecs,
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func appendStepSummary(r *reportData) error {
	summaryPath := os.Getenv("GITHUB_STEP_SUMMARY")
	if summaryPath == "" {
		return nil
	}
	f, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "<h1>E2E Health Report</h1>\n")
	fmt.Fprintf(&b, "<p>Generated: %s &nbsp;·&nbsp; %d runs analyzed</p>\n", r.GeneratedAt, r.AnalyzedRuns)

	fmt.Fprintf(&b, "<h2>Pipeline Timing</h2>\n")
	fmt.Fprintf(&b, "<table><thead><tr><th>Phase</th><th>Avg</th><th>±StdDev</th></tr></thead><tbody>\n")
	for _, p := range r.PipelinePhases {
		indent := ""
		if p.Indent > 0 {
			indent = "&nbsp;&nbsp;&nbsp;"
		}
		fmt.Fprintf(&b, "<tr><td>%s%s</td><td>%s</td><td>±%s</td></tr>\n",
			indent, htmlEsc(p.Name), formatDuration(p.AvgSecs), formatDuration(p.StdDev))
	}
	fmt.Fprintf(&b, "</tbody></table>\n")

	fmt.Fprintf(&b, "<h2>Flaky Tests</h2>\n")
	fmt.Fprintf(&b, "<p>%d specs — %d flaky, %d consistently failing, %d infra events excluded</p>\n",
		r.TotalAnalyzedSpecs, r.FlakyCount, r.ConsistentlyFailingCount, r.InfraInstabilityCount)
	if len(r.FlakeEntries) > 0 {
		fmt.Fprintf(&b, "<table><thead><tr><th>Rate</th><th>Class</th><th>Last Failed</th><th>Test</th></tr></thead><tbody>\n")
		for _, fe := range r.FlakeEntries {
			label := fe.SpecName
			if fe.GinkgoLabel != "" {
				label = fmt.Sprintf("[%s] %s", fe.GinkgoLabel, fe.SpecName)
			}
			fmt.Fprintf(&b, "<tr><td>%.0f%% (%d/%d)</td><td>%s</td><td><a href=%q>run</a></td><td>%s</td></tr>\n",
				fe.FailRate*100, fe.FailCount, fe.TotalRuns, htmlEsc(fe.Class), fe.LastRunURL, htmlEsc(label))
		}
		fmt.Fprintf(&b, "</tbody></table>\n")
	}

	if len(r.OptimEntries) > 0 {
		fmt.Fprintf(&b, "<h2>Optimization Opportunities</h2>\n")
		saving := r.OptimBaselineWorkflowSecs - r.OptimBestWorkflowSecs
		fmt.Fprintf(&b, "<p>Top %d slow tests: potential workflow saving <strong>%s</strong> (LPT simulation).</p>\n",
			len(r.OptimEntries), formatDuration(saving))
		fmt.Fprintf(&b, "<table><thead><tr><th>Test</th><th>Current Avg</th><th>Potential Saving</th></tr></thead><tbody>\n")
		for _, o := range r.OptimEntries {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>−%s</td></tr>\n",
				htmlEsc(o.Name), formatDuration(o.AvgSecs), formatDuration(o.SavedSecs))
		}
		fmt.Fprintf(&b, "</tbody></table>\n")
	}

	_, err = f.WriteString(b.String())
	return err
}

// formatDuration converts seconds to a human-readable string.
func formatDuration(secs float64) string {
	if secs <= 0 {
		return "0s"
	}
	if secs < 60 {
		return fmt.Sprintf("%.0fs", secs)
	}
	m := int(secs) / 60
	s := int(secs) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

func htmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&#34;")
	return s
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// SVG chart generators

type donutSegment struct {
	Label string
	Value float64
	Color string
}

// svgDonut generates a self-contained SVG donut chart.
func svgDonut(segments []donutSegment, size int) template.HTML {
	var total float64
	for _, s := range segments {
		total += s.Value
	}
	if total == 0 {
		return ""
	}
	cx, cy := float64(size)/2, float64(size)/2
	outerR := cx * 0.72
	innerR := outerR * 0.56
	legendX := size + 14

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" style="max-width:%dpx">`, size+190, size, size+190)

	startAngle := -math.Pi / 2
	for i, seg := range segments {
		if seg.Value == 0 {
			continue
		}
		sweep := seg.Value / total * 2 * math.Pi
		endAngle := startAngle + sweep

		x1 := cx + outerR*math.Cos(startAngle)
		y1 := cy + outerR*math.Sin(startAngle)
		x2 := cx + outerR*math.Cos(endAngle)
		y2 := cy + outerR*math.Sin(endAngle)
		x3 := cx + innerR*math.Cos(endAngle)
		y3 := cy + innerR*math.Sin(endAngle)
		x4 := cx + innerR*math.Cos(startAngle)
		y4 := cy + innerR*math.Sin(startAngle)

		largeArc := 0
		if sweep > math.Pi {
			largeArc = 1
		}
		fmt.Fprintf(&buf,
			`<path d="M %.2f %.2f A %.2f %.2f 0 %d 1 %.2f %.2f L %.2f %.2f A %.2f %.2f 0 %d 0 %.2f %.2f Z" fill="%s"/>`,
			x1, y1, outerR, outerR, largeArc, x2, y2, x3, y3, innerR, innerR, largeArc, x4, y4, seg.Color)

		ly := float64(20 + i*26)
		fmt.Fprintf(&buf, `<rect x="%d" y="%.0f" width="14" height="14" rx="2" fill="%s"/>`, legendX, ly, seg.Color)
		fmt.Fprintf(&buf,
			`<text x="%d" y="%.0f" font-size="12" style="fill:var(--text)" font-family="system-ui,sans-serif">%.0f%% %s (%.0f)</text>`,
			legendX+18, ly+11, seg.Value/total*100, htmlEsc(seg.Label), seg.Value)

		startAngle = endAngle
	}

	fmt.Fprintf(&buf,
		`<text x="%.0f" y="%.0f" font-size="16" font-weight="700" text-anchor="middle" style="fill:var(--text)" font-family="system-ui,sans-serif">%.0f</text>`,
		cx, cy+6, total)

	buf.WriteString("</svg>")
	return template.HTML(buf.String()) //nolint:gosec
}

// svgBars renders a horizontal bar chart from parallel slices.
func svgBars(labels []string, values []float64, stddevs []float64, colors []string, indents []int, svgWidth, rowH int) template.HTML {
	if len(labels) == 0 {
		return ""
	}
	const labelW = 220
	const valW = 130 // wide enough for "17m 52s ±1m 3s"
	const gap = 6
	barW := svgWidth - labelW - valW - 12

	maxVal := 0.0
	for _, v := range values {
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	var buf bytes.Buffer
	svgH := len(labels)*(rowH+gap) + 12
	fmt.Fprintf(&buf, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" style="max-width:%dpx">`, svgWidth, svgH, svgWidth)

	for i, label := range labels {
		isIndented := i < len(indents) && indents[i] > 0

		// Child rows use a slightly thinner bar and muted colours; the "↳"
		// prefix already in the label name conveys the hierarchy.
		thisRowH := rowH
		labelFill := "var(--text)"
		fontSize := 12
		if isIndented {
			thisRowH = rowH - 4
			labelFill = "var(--muted)"
			fontSize = 11
		}

		y := i*(rowH+gap) + 6
		barYOff := (rowH - thisRowH) / 2
		midY := y + rowH/2 + 4
		barPx := int(values[i] / maxVal * float64(barW))

		color := "#3b82f6"
		if isIndented {
			color = "#93c5fd"
		}
		if i < len(colors) && colors[i] != "" {
			color = colors[i]
		}

		fmt.Fprintf(&buf,
			`<text x="4" y="%d" font-size="%d" text-anchor="start" style="fill:%s" font-family="system-ui,sans-serif">%s</text>`,
			midY, fontSize, labelFill, htmlEsc(truncateStr(label, 32)))
		fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="2" style="fill:var(--bar-bg)"/>`, labelW, y+barYOff, barW, thisRowH)
		if barPx > 0 {
			fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="2" fill="%s"/>`, labelW, y+barYOff, barPx, thisRowH, color)
		}
		valLabel := formatDuration(values[i])
		if i < len(stddevs) && stddevs[i] > 0 {
			valLabel += " ±" + formatDuration(stddevs[i])
		}
		fmt.Fprintf(&buf,
			`<text x="%d" y="%d" font-size="11" style="fill:var(--muted)" font-family="system-ui,sans-serif">%s</text>`,
			labelW+barW+4, midY, valLabel)
	}

	buf.WriteString("</svg>")
	return template.HTML(buf.String()) //nolint:gosec
}

func svgPipelineBars(phases []phaseEntry) template.HTML {
	labels := make([]string, len(phases))
	values := make([]float64, len(phases))
	stddevs := make([]float64, len(phases))
	colors := make([]string, len(phases))
	indents := make([]int, len(phases))
	for i, p := range phases {
		labels[i] = p.Name
		values[i] = p.AvgSecs
		stddevs[i] = p.StdDev
		indents[i] = p.Indent
		if p.Indent > 0 {
			colors[i] = "#93c5fd"
		} else {
			colors[i] = "#3b82f6"
		}
	}
	return svgBars(labels, values, stddevs, colors, indents, 560, 28)
}

func svgSpecBars(specs []specEntry, color string) template.HTML {
	labels := make([]string, len(specs))
	values := make([]float64, len(specs))
	stddevs := make([]float64, len(specs))
	colors := make([]string, len(specs))
	for i, s := range specs {
		labels[i] = s.Name
		values[i] = s.AvgSecs
		stddevs[i] = s.StdDev
		colors[i] = color
	}
	return svgBars(labels, values, stddevs, colors, nil, 560, 22)
}
