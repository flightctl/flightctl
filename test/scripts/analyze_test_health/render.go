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
	FlakeDonutSVG   template.HTML
	SlowestChartSVG template.HTML
	TrendChartSVG   template.HTML
}

type slackSummary struct {
	Title                   string  `json:"title"`
	Workflow                string  `json:"workflow"`
	AnalyzedRuns            int     `json:"analyzed_runs"`
	TotalSpecsTracked       int     `json:"total_specs_tracked"`
	ConsistentlyFailing     int     `json:"consistently_failing"`
	FlakyTests              int     `json:"flaky_tests"`
	NeverFailed             int     `json:"never_failed"`
	InfraInstabilityEvents  int     `json:"infra_instability_events"`
}

// renderHTML generates all SVG charts, builds a renderContext, and executes
// the embedded HTML template, writing the result to path.
func renderHTML(data *reportData, path string) error {
	ctx := &renderContext{
		reportData: data,
		FlakeDonutSVG: svgDonut([]donutSegment{
			{Label: "Clean", Value: float64(data.CleanCount), Color: "#22c55e"},
			{Label: "Flaky", Value: float64(data.FlakyCount), Color: "#f59e0b"},
			{Label: "Consistently failing", Value: float64(data.ConsistentlyFailingCount), Color: "#ef4444"},
		}, 260),
		SlowestChartSVG: svgSpecBars(data.SlowestTests, "#f59e0b"),
		TrendChartSVG:   svgTrendBars(data.TrendEntries),
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
		Title:                  r.Title,
		Workflow:               r.Workflow,
		AnalyzedRuns:           r.AnalyzedRuns,
		TotalSpecsTracked:      r.TotalAnalyzedSpecs,
		ConsistentlyFailing:    r.ConsistentlyFailingCount,
		FlakyTests:             r.FlakyCount,
		NeverFailed:            r.CleanCount,
		InfraInstabilityEvents: r.InfraInstabilityCount,
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
	fmt.Fprintf(&b, "<h1>%s</h1>\n", htmlEsc(r.Title))
	fmt.Fprintf(&b, "<p>Generated: %s &nbsp;·&nbsp; %d runs analyzed</p>\n", r.GeneratedAt, r.AnalyzedRuns)

	fmt.Fprintf(&b, "<h2>Test Health</h2>\n")
	fmt.Fprintf(&b, "<p>%d specs — %d flaky, %d consistently failing, %d clean",
		r.TotalAnalyzedSpecs, r.FlakyCount, r.ConsistentlyFailingCount, r.CleanCount)
	if r.InfraInstabilityCount > 0 {
		fmt.Fprintf(&b, " (%d infra instability runs excluded)", r.InfraInstabilityCount)
	}
	fmt.Fprintf(&b, "</p>\n")

	if len(r.FlakeEntries) > 0 {
		fmt.Fprintf(&b, "<table><thead><tr><th>Rate</th><th>Class</th><th>Last Failed</th><th>Test</th></tr></thead><tbody>\n")
		for _, fe := range r.FlakeEntries {
			fmt.Fprintf(&b, "<tr><td>%.0f%% (%d/%d)</td><td>%s</td><td><a href=%q>run</a></td><td>%s</td></tr>\n",
				fe.FailRate*100, fe.FailCount, fe.TotalRuns, htmlEsc(fe.Class), fe.LastRunURL, htmlEsc(fe.SpecName))
		}
		fmt.Fprintf(&b, "</tbody></table>\n")
	}

	if len(r.SlowestTests) > 0 {
		fmt.Fprintf(&b, "<h2>Slowest Tests</h2>\n")
		fmt.Fprintf(&b, "<table><thead><tr><th>Avg</th><th>±StdDev</th><th>Test</th></tr></thead><tbody>\n")
		for _, s := range r.SlowestTests {
			fmt.Fprintf(&b, "<tr><td>%s</td><td>±%s</td><td>%s</td></tr>\n",
				formatDuration(s.AvgSecs), formatDuration(s.StdDev), htmlEsc(s.Name))
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
func svgBars(labels []string, values []float64, stddevs []float64, colors []string, svgWidth, rowH int) template.HTML {
	if len(labels) == 0 {
		return ""
	}
	const labelW = 260
	const valW = 100
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
		y := i*(rowH+gap) + 6
		midY := y + rowH/2 + 4
		barPx := int(values[i] / maxVal * float64(barW))

		color := "#3b82f6"
		if i < len(colors) && colors[i] != "" {
			color = colors[i]
		}

		fmt.Fprintf(&buf,
			`<text x="4" y="%d" font-size="12" text-anchor="start" style="fill:var(--text)" font-family="system-ui,sans-serif">%s</text>`,
			midY, htmlEsc(truncateStr(label, 38)))
		fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="2" style="fill:var(--bar-bg)"/>`, labelW, y, barW, rowH)
		if barPx > 0 {
			fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="2" fill="%s"/>`, labelW, y, barPx, rowH, color)
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
	return svgBars(labels, values, stddevs, colors, 560, 22)
}

// svgTrendBars renders a stacked vertical bar chart showing per-run pass/fail counts.
func svgTrendBars(trend []trendEntry) template.HTML {
	if len(trend) == 0 {
		return ""
	}

	const (
		barW    = 32
		barGap  = 6
		maxBarH = 120
		labelH  = 32
		svgPad  = 10
	)

	// Find the max total to scale bars.
	maxTotal := 0
	for _, t := range trend {
		if t.Total > maxTotal {
			maxTotal = t.Total
		}
	}
	if maxTotal == 0 {
		maxTotal = 1
	}

	svgW := len(trend)*(barW+barGap) + svgPad*2
	svgH := maxBarH + labelH + svgPad

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="100%%" style="max-width:%dpx">`, svgW, svgH, svgW)

	for i, t := range trend {
		x := svgPad + i*(barW+barGap)
		if t.Total == 0 {
			// No JUnit data for this run — draw a gray placeholder.
			fmt.Fprintf(&buf,
				`<rect x="%d" y="%d" width="%d" height="%d" rx="2" fill="#d1d5db" opacity="0.4"/>`,
				x, svgPad+maxBarH/2, barW, maxBarH/2)
		} else {
			totalH := int(float64(t.Total) / float64(maxTotal) * float64(maxBarH))
			if totalH < 2 {
				totalH = 2
			}
			passH := int(float64(t.Passed) / float64(t.Total) * float64(totalH))
			failH := int(float64(t.Failed) / float64(t.Total) * float64(totalH))
			skipH := totalH - passH - failH
			if skipH < 0 {
				skipH = 0
			}

			yTop := svgPad + maxBarH - totalH
			// Draw fail (red), skip (gray), pass (green) — bottom up.
			yPass := yTop
			if passH > 0 {
				fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="0" fill="#22c55e"/>`, x, yPass, barW, passH)
			}
			ySkip := yPass + passH
			if skipH > 0 {
				fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="0" fill="#d1d5db"/>`, x, ySkip, barW, skipH)
			}
			yFail := ySkip + skipH
			if failH > 0 {
				fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="%d" height="%d" rx="0" fill="#ef4444"/>`, x, yFail, barW, failH)
			}
		}

		// Date label, rotated.
		labelY := svgPad + maxBarH + 4
		dateShort := t.Date
		if len(dateShort) >= 10 {
			dateShort = dateShort[5:] // "MM-DD"
		}
		linkOpen := ""
		linkClose := ""
		if t.RunURL != "" {
			linkOpen = fmt.Sprintf(`<a href="%s" target="_blank">`, htmlEsc(t.RunURL))
			linkClose = `</a>`
		}
		fmt.Fprintf(&buf,
			`%s<text x="%d" y="%d" font-size="9" text-anchor="end" transform="rotate(-55,%d,%d)" style="fill:var(--muted)" font-family="system-ui,sans-serif">%s</text>%s`,
			linkOpen, x+barW/2, labelY, x+barW/2, labelY, htmlEsc(dateShort), linkClose)
	}

	// Legend
	legendY := svgH - 4
	fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="10" height="10" fill="#22c55e"/>`, svgPad, legendY-10)
	fmt.Fprintf(&buf, `<text x="%d" y="%d" font-size="10" style="fill:var(--muted)" font-family="system-ui,sans-serif">Pass</text>`, svgPad+13, legendY)
	fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="10" height="10" fill="#ef4444"/>`, svgPad+50, legendY-10)
	fmt.Fprintf(&buf, `<text x="%d" y="%d" font-size="10" style="fill:var(--muted)" font-family="system-ui,sans-serif">Fail</text>`, svgPad+63, legendY)
	fmt.Fprintf(&buf, `<rect x="%d" y="%d" width="10" height="10" fill="#d1d5db"/>`, svgPad+100, legendY-10)
	fmt.Fprintf(&buf, `<text x="%d" y="%d" font-size="10" style="fill:var(--muted)" font-family="system-ui,sans-serif">Skip/missing</text>`, svgPad+113, legendY)

	buf.WriteString("</svg>")
	return template.HTML(buf.String()) //nolint:gosec
}
