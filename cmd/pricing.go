package cmd

import (
	"regexp"
	"strconv"
	"strings"
)

// pqlMetricSelector matches a PromQL metric name immediately followed by a
// label selector: e.g. kube_job_status_failed{...} or
// job_cronjob:kube_job_status_start_time:max{...}
var pqlMetricSelector = regexp.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:]+)\{`)

// Google Cloud Monitoring alert pricing (effective Sept 1, 2026).
// https://cloud.google.com/products/observability/pricing
const (
	metricReferenceCostUSD    = 0.35
	pointsCostPerMillionUSD   = 0.50
	defaultExecutionPeriodSec = int64(30)
	billingDaysPerMonth       = 30
)

func executionPeriodSeconds(seconds int64) int64 {
	if seconds <= 0 {
		return defaultExecutionPeriodSec
	}
	return seconds
}

func periodsPerMonth(executionPeriodSec int64) float64 {
	return float64(billingDaysPerMonth*24*3600) / float64(executionPeriodSeconds(executionPeriodSec))
}

func monthlyMetricReferenceCost(metricReferences int) float64 {
	if metricReferences <= 0 {
		return 0
	}
	return float64(metricReferences) * metricReferenceCostUSD
}

func monthlyPointsCost(pointsPerExecution int, executionPeriodSec int64) float64 {
	if pointsPerExecution <= 0 {
		return 0
	}
	return float64(pointsPerExecution) * periodsPerMonth(executionPeriodSec) * pointsCostPerMillionUSD / 1_000_000
}

// promqlLogicalOps are PromQL's vector-matching keywords. Google splits a
// PromQL condition into subqueries on each occurrence of these, plus the
// threshold/comparison operators below.
// https://cloud.google.com/products/observability/pricing#pricing-alerting
var promqlLogicalOps = []string{"and", "or", "unless"}

// promqlComparisonOps are PromQL's threshold operators, ordered so that
// two-character operators are matched before their single-character prefixes
// (">=" before ">", etc).
var promqlComparisonOps = []string{">=", "<=", "==", "!=", ">", "<"}

var scalarLiteralPattern = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// mqlEveryPattern matches MQL's "| every <duration>" clause, e.g. "| every 1m",
// "| every 30s", "| every 1h". This clause sets the resampling period that
// determines how often the condition is effectively (re-)evaluated.
var mqlEveryPattern = regexp.MustCompile(`(?i)\|\s*every\s+(\d+)\s*([smhd])`)

// mqlEvaluationIntervalSeconds extracts the evaluation interval from an MQL
// query's "| every X" clause. Falls back to defaultExecutionPeriodSec when no
// such clause is present.
func mqlEvaluationIntervalSeconds(query string) int64 {
	m := mqlEveryPattern.FindStringSubmatch(query)
	if m == nil {
		return defaultExecutionPeriodSec
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return defaultExecutionPeriodSec
	}
	switch m[2] {
	case "s":
		return n
	case "m":
		return n * 60
	case "h":
		return n * 3600
	case "d":
		return n * 86400
	default:
		return defaultExecutionPeriodSec
	}
}

// splitPromQLSubqueries splits a PromQL expression into the subqueries Google
// bills separately: one per side of every top-level logical operator (and/or/
// unless) and comparison operator (>, <, >=, <=, ==, !=). "Top-level" means
// outside of any parentheses, brackets, label-selector braces, or string
// literal, so operators nested inside a function call or label matcher (e.g.
// `foo{label!="x"}`) are left untouched.
func splitPromQLSubqueries(query string) []string {
	var (
		parenDepth, braceDepth, bracketDepth int
		inString                             bool
		segments                             []string
		segStart                             int
	)

	runes := []rune(query)
	n := len(runes)

	atTopLevel := func() bool {
		return parenDepth == 0 && braceDepth == 0 && bracketDepth == 0
	}

	for i := 0; i < n; {
		c := runes[i]

		if inString {
			if c == '"' {
				inString = false
			}
			i++
			continue
		}
		switch c {
		case '"':
			inString = true
			i++
			continue
		case '(':
			parenDepth++
		case ')':
			parenDepth--
		case '{':
			braceDepth++
		case '}':
			braceDepth--
		case '[':
			bracketDepth++
		case ']':
			bracketDepth--
		}

		if atTopLevel() {
			if op := matchSymbolicOpAt(runes, i); op != "" {
				segments = append(segments, strings.TrimSpace(string(runes[segStart:i])))
				i += len([]rune(op))
				segStart = i
				continue
			}
			if op := matchWordOpAt(runes, i); op != "" {
				segments = append(segments, strings.TrimSpace(string(runes[segStart:i])))
				i += len([]rune(op))
				segStart = i
				continue
			}
		}
		i++
	}
	segments = append(segments, strings.TrimSpace(string(runes[segStart:])))

	out := make([]string, 0, len(segments))
	for _, s := range segments {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func matchSymbolicOpAt(runes []rune, i int) string {
	for _, op := range promqlComparisonOps {
		opRunes := []rune(op)
		if i+len(opRunes) > len(runes) {
			continue
		}
		if string(runes[i:i+len(opRunes)]) == op {
			return op
		}
	}
	return ""
}

func matchWordOpAt(runes []rune, i int) string {
	for _, op := range promqlLogicalOps {
		opRunes := []rune(op)
		end := i + len(opRunes)
		if end > len(runes) {
			continue
		}
		if !strings.EqualFold(string(runes[i:end]), op) {
			continue
		}
		beforeOK := i == 0 || !isIdentRune(runes[i-1])
		afterOK := end == len(runes) || !isIdentRune(runes[end])
		if beforeOK && afterOK {
			return op
		}
	}
	return ""
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// billablePromQLSubqueries returns the subqueries of a PromQL expression that
// Google actually bills points for. Pure scalar literals (e.g. the "10800" in
// "... > 10800") are dropped because a bare number returns no time series.
func billablePromQLSubqueries(query string) []string {
	segments := splitPromQLSubqueries(query)
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		if s == "" || scalarLiteralPattern.MatchString(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// countMetricReferencesInQuery counts billable metric references in a query.
//
// Per Google's pricing definitions, "a metric reference is a single mention
// of a metric name within an alerting policy condition" and PromQL/MQL
// conditions "have as many metric references as are used to construct the
// query" — i.e. every textual mention is billed, even if the same metric name
// is referenced multiple times with different label filters (e.g. an
// error-rate ratio that divides one filtered view of a metric by another).
// This was validated against a live alert: a query referencing the same
// PromQL metric name twice (different label filters, for a numerator and a
// denominator) billed 2 metric references ($0.70), not 1.
//   - MQL / threshold filter: each "metric.type" mention is one reference.
//   - PromQL: each metric selector occurrence (identifier before "{") is one
//     reference, including repeats of the same metric name.
//
// Falls back to 1 when neither pattern is detected (e.g. a bare fetch expression).
func countMetricReferencesInQuery(query string) int {
	if strings.TrimSpace(query) == "" {
		return 0
	}
	// Cloud Monitoring filter / MQL syntax: metric.type = "..."
	if n := strings.Count(strings.ToLower(query), "metric.type"); n > 0 {
		return n
	}
	// PromQL syntax: count every metric selector occurrence (metric_name{labels...}).
	if n := len(pqlMetricSelector.FindAllString(query, -1)); n > 0 {
		return n
	}
	return 1
}
