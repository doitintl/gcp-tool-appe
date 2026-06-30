package cmd

import "testing"

func TestPeriodsPerMonthDefaultInterval(t *testing.T) {
	const want = 86400.0
	if got := periodsPerMonth(defaultExecutionPeriodSec); got != want {
		t.Fatalf("periodsPerMonth(30s) = %v, want %v", got, want)
	}
}

func TestMonthlyMetricReferenceCost(t *testing.T) {
	if got := monthlyMetricReferenceCost(1); got != 0.35 {
		t.Fatalf("monthlyMetricReferenceCost(1) = %v, want 0.35", got)
	}
	if got := monthlyMetricReferenceCost(2); got != 0.70 {
		t.Fatalf("monthlyMetricReferenceCost(2) = %v, want 0.70", got)
	}
}

func TestMonthlyPointsCostMatchesGoogleExample(t *testing.T) {
	// 100 points/period * 86,400 periods * $0.50/1M = $4.32
	const want = 4.32
	if got := monthlyPointsCost(100, defaultExecutionPeriodSec); got != want {
		t.Fatalf("monthlyPointsCost(100, 30s) = %v, want %v", got, want)
	}
}

func TestMonthlyPointsCostSingleTimeSeries(t *testing.T) {
	// 1 point/period * 86,400 periods * $0.50/1M = $0.0432
	const want = 0.0432
	if got := monthlyPointsCost(1, defaultExecutionPeriodSec); got != want {
		t.Fatalf("monthlyPointsCost(1, 30s) = %v, want %v", got, want)
	}
}

func TestMonthlyPointsCostPromQLInterval(t *testing.T) {
	// 15-second evaluation interval doubles monthly periods.
	const want = 0.0864
	if got := monthlyPointsCost(1, 15); got != want {
		t.Fatalf("monthlyPointsCost(1, 15s) = %v, want %v", got, want)
	}
}

func TestCountMetricReferencesInQuery(t *testing.T) {
	tests := []struct {
		query string
		want  int
	}{
		{"", 0},
		{"fetch gce_instance", 1},
		{`metric.type = "a" AND metric.type = "b"`, 2},
		{`resource.type="k8s_container" AND metric.type="kubernetes.io/container/cpu/core_usage_time"`, 1},
	}
	for _, tc := range tests {
		if got := countMetricReferencesInQuery(tc.query); got != tc.want {
			t.Fatalf("countMetricReferencesInQuery(%q) = %d, want %d", tc.query, got, tc.want)
		}
	}
}
