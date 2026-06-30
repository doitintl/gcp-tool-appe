package cmd

import "testing"

func TestPeriodsPerMonthDefaultInterval(t *testing.T) {
	if got := periodsPerMonth(defaultExecutionPeriodSec); got != 86400.0 {
		t.Fatalf("periodsPerMonth(30s) = %v, want 86400", got)
	}
}

func TestMonthlyMetricReferenceCost(t *testing.T) {
	cases := []struct{ refs int; want float64 }{
		{1, 0.35},
		{2, 0.70},
		{0, 0.0},
	}
	for _, c := range cases {
		if got := monthlyMetricReferenceCost(c.refs); got != c.want {
			t.Fatalf("monthlyMetricReferenceCost(%d) = %v, want %v", c.refs, got, c.want)
		}
	}
}

// TestMonthlyPointsCostMatchesGoogleExample uses the canonical example from
// https://cloud.google.com/stackdriver/docs/observability/pricing-examples:
// 100 points/period × 86,400 periods × $0.50/1M = $4.32
func TestMonthlyPointsCostMatchesGoogleExample(t *testing.T) {
	if got := monthlyPointsCost(100, defaultExecutionPeriodSec); got != 4.32 {
		t.Fatalf("monthlyPointsCost(100, 30s) = %v, want 4.32", got)
	}
}

func TestMonthlyPointsCostSinglePoint(t *testing.T) {
	// 1 × 86,400 × $0.50/1M = $0.0432
	if got := monthlyPointsCost(1, defaultExecutionPeriodSec); got != 0.0432 {
		t.Fatalf("monthlyPointsCost(1, 30s) = %v, want 0.0432", got)
	}
}

func TestMonthlyPointsCostShorterInterval(t *testing.T) {
	// 15s interval → 2× periods → $0.0864
	if got := monthlyPointsCost(1, 15); got != 0.0864 {
		t.Fatalf("monthlyPointsCost(1, 15s) = %v, want 0.0864", got)
	}
}

func TestAlignmentWindowDefaultsToExecutionPeriod(t *testing.T) {
	if got := alignmentWindowSeconds(nil); got != defaultExecutionPeriodSec {
		t.Fatalf("alignmentWindowSeconds(nil) = %d, want %d", got, defaultExecutionPeriodSec)
	}
}

func TestCountMetricReferencesInQuery(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", 0},
		{"fetch gce_instance", 1},
		{`metric.type = "a" AND metric.type = "b"`, 2},
		{`resource.type="k8s_container" AND metric.type="kubernetes.io/container/cpu/core_usage_time"`, 1},
	}
	for _, c := range cases {
		if got := countMetricReferencesInQuery(c.query); got != c.want {
			t.Fatalf("countMetricReferencesInQuery(%q) = %d, want %d", c.query, got, c.want)
		}
	}
}

// TestDocumentsUiAlertExpectedCost validates the arithmetic for the test alert:
// "[weber] documents-ui - cpu usage high"
//   - 1 threshold condition → 1 metric reference → $0.35
//   - REDUCE_MEAN: 1 series; alignmentPeriod=300s: 1 point per execution
//   - 1 pt/exec × 86,400 exec/mo × $0.50/1M = $0.0432
//   - Total = $0.3932
func TestDocumentsUiAlertExpectedCost(t *testing.T) {
	refCost := monthlyMetricReferenceCost(1)
	ptsCost := monthlyPointsCost(1, defaultExecutionPeriodSec)
	total := refCost + ptsCost
	if total != 0.3932 {
		t.Fatalf("total cost = %v, want 0.3932", total)
	}
}
