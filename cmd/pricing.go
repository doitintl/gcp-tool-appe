package cmd

import (
	"regexp"
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

// countMetricReferencesInQuery counts billable metric references in a query.
//
// Google bills one metric reference per distinct metric name used in a condition:
//   - MQL / threshold filter: each "metric.type" mention is one reference.
//   - PromQL: each distinct metric selector (identifier before "{") is one reference.
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
	// PromQL syntax: count distinct metric selectors (metric_name{labels...})
	matches := pqlMetricSelector.FindAllStringSubmatch(query, -1)
	if len(matches) > 0 {
		seen := make(map[string]struct{}, len(matches))
		for _, m := range matches {
			seen[m[1]] = struct{}{}
		}
		return len(seen)
	}
	return 1
}
