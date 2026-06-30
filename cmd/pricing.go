package cmd

import "strings"

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

// countMetricReferencesInQuery estimates metric references for MQL/PromQL conditions.
// Each mention of metric.type counts as one reference; otherwise assume one reference.
func countMetricReferencesInQuery(query string) int {
	if strings.TrimSpace(query) == "" {
		return 0
	}
	if n := strings.Count(strings.ToLower(query), "metric.type"); n > 0 {
		return n
	}
	return 1
}
