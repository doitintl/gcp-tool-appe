package cmd

import "testing"

func TestPeriodsPerMonthDefaultInterval(t *testing.T) {
	if got := periodsPerMonth(defaultExecutionPeriodSec); got != 86400.0 {
		t.Fatalf("periodsPerMonth(30s) = %v, want 86400", got)
	}
}

func TestMonthlyMetricReferenceCost(t *testing.T) {
	cases := []struct {
		refs int
		want float64
	}{
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
		name  string
		query string
		want  int
	}{
		{"empty", "", 0},
		{"mql bare fetch, no metric.type → default 1", "fetch gce_instance", 1},
		{"mql two metric.type mentions", `metric.type = "a" AND metric.type = "b"`, 2},
		{"threshold filter one metric.type", `resource.type="k8s_container" AND metric.type="kubernetes.io/container/cpu/core_usage_time"`, 1},
		// PromQL selectors
		{"promql single metric", `kube_pod_status_phase{phase="Running"} > 0`, 1},
		{"promql two distinct metrics", `job_cronjob:kube_job_status_start_time:max{exported_namespace!=""} * ON(job) kube_job_status_failed{exported_namespace!=""} > 0`, 2},
		// Each mention of a metric name is its own reference, even with
		// different label filters — validated against a live alert where
		// dividing two differently filtered views of the same metric
		// (a common error-rate-ratio pattern) billed 2 references ($0.70).
		{"promql same metric name, different filters, counts each mention", `rate(http_requests_total{code="200"}[5m]) / rate(http_requests_total{code!=""}[5m])`, 2},
		// A real-world cronjob-failure query joining two distinct metrics via
		// a vector-matching multiplication.
		{"promql two metrics joined via vector matching — 2 distinct metrics",
			`sum by (cronjob) (clamp_max(topk by(cronjob) (1, job_cronjob:kube_job_status_start_time:max{exported_namespace!=""}), 1) * ON(job, exported_namespace) GROUP_LEFT() label_replace(label_replace((sum without(instance) (kube_job_status_failed{exported_namespace!=""} != 0)), "job", "$1", "job_name", "(.+)"), "cronjob", "$1", "label_app_kubernetes_io_name", "(.+)")) > 0`,
			2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countMetricReferencesInQuery(c.query); got != c.want {
				t.Fatalf("got %d, want %d for query: %s", got, c.want, c.query)
			}
		})
	}
}

func TestMqlEvaluationIntervalSeconds(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{"every 1m", `fetch pubsub_subscription | metric 'x' | every 1m | within 1h`, 60},
		{"every 30s", `fetch x | every 30s`, 30},
		{"every 1h", `fetch x | every 1h`, 3600},
		{"every 1d", `fetch x | every 1d`, 86400},
		{"no every clause defaults to default execution period", `fetch x | within 1h`, defaultExecutionPeriodSec},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mqlEvaluationIntervalSeconds(c.query)
			if got != c.want {
				t.Fatalf("mqlEvaluationIntervalSeconds(%q) = %d, want %d", c.query, got, c.want)
			}
		})
	}
}

func TestSplitPromQLSubqueries(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "simple threshold comparison",
			query: `max_over_time(foo_metric{label="x"}[1m]) > 10800`,
			want:  []string{`max_over_time(foo_metric{label="x"}[1m])`, "10800"},
		},
		{
			name:  "label selector != is not split (inside braces)",
			query: `foo{label!="x"} > 0`,
			want:  []string{`foo{label!="x"}`, "0"},
		},
		{
			name:  "nested != inside parens is not split at top level",
			query: `sum(bar{label!=""} != 0) > 0`,
			want:  []string{`sum(bar{label!=""} != 0)`, "0"},
		},
		{
			name:  "logical and at top level",
			query: `up{job="a"} == 1 and rate(errors[5m]) > 0`,
			want:  []string{`up{job="a"}`, "1", `rate(errors[5m])`, "0"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitPromQLSubqueries(c.query)
			if len(got) != len(c.want) {
				t.Fatalf("got %d segments %v, want %d segments %v", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("segment %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestBillablePromQLSubqueriesDropsScalarLiterals(t *testing.T) {
	query := `max_over_time(foo_metric{label="x"}[1m]) > 10800`
	got := billablePromQLSubqueries(query)
	want := []string{`max_over_time(foo_metric{label="x"}[1m])`}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("billablePromQLSubqueries(%q) = %v, want %v", query, got, want)
	}
}

func TestBillablePromQLSubqueriesKeepsBothVectorSides(t *testing.T) {
	// Neither side of a vector-to-vector comparison is a scalar, so both
	// are billable subqueries.
	query := `metric_a{} > metric_b{}`
	got := billablePromQLSubqueries(query)
	if len(got) != 2 {
		t.Fatalf("billablePromQLSubqueries(%q) = %v, want 2 subqueries", query, got)
	}
}

// TestErrorRateRatioAlertExpectedCost validates the exact expected cost for
// an error-rate-ratio alert (numerator and denominator both filter the same
// metric) against the GCP console's cost preview UI: $0.70 (2 metric
// references — the same metric mentioned twice, once per side of the ratio —
// rounds out the negligible points cost to $0.00).
func TestErrorRateRatioAlertExpectedCost(t *testing.T) {
	query := `(sum by (method,path)(increase(http_server_requests_total{status=~"(4|5).."}[30m])))
/
(sum by (method,path)(increase(http_server_requests_total{}[30m])))
> 0.3
`
	refs := countMetricReferencesInQuery(query)
	if refs != 2 {
		t.Fatalf("countMetricReferencesInQuery = %d, want 2 (same metric mentioned twice)", refs)
	}
	refCost := monthlyMetricReferenceCost(refs)
	if refCost != 0.70 {
		t.Fatalf("refCost = %v, want 0.70", refCost)
	}
}

// TestPubsubUnackedAlertExpectedCost validates the exact expected cost for a
// pubsub-unacked-message-age alert against the value shown in the GCP
// console's cost preview UI: $0.35 (1 metric ref) + $0.02 (1 time series ×
// 43,200 evaluations/month × $0.50/1M) = $0.37.
func TestPubsubUnackedAlertExpectedCost(t *testing.T) {
	query := `max_over_time(pubsub_googleapis_com:subscription_oldest_unacked_message_age{subscription_id="x"}[1m]) > 10800`
	evaluationIntervalSec := int64(60)

	refs := countMetricReferencesInQuery(query)
	if refs != 1 {
		t.Fatalf("countMetricReferencesInQuery = %d, want 1", refs)
	}
	refCost := monthlyMetricReferenceCost(refs)

	subqueries := billablePromQLSubqueries(query)
	if len(subqueries) != 1 {
		t.Fatalf("billablePromQLSubqueries = %v, want exactly 1 subquery (the scalar threshold must be dropped)", subqueries)
	}

	// The metric is always present (1 time series, 1 point per execution),
	// matching what the console UI reports for this alert.
	const pointsPerExecution = 1
	pointsCost := monthlyPointsCost(pointsPerExecution, evaluationIntervalSec)

	total := refCost + pointsCost
	const want = 0.3716 // GCP console rounds this to $0.37
	const epsilon = 0.0001
	if total < want-epsilon || total > want+epsilon {
		t.Fatalf("total = %v, want ~%v", total, want)
	}
}

// TestThresholdAlertExpectedCost validates the arithmetic for a typical
// CPU-usage threshold alert:
//   - 1 threshold condition → 1 metric reference → $0.35
//   - REDUCE_MEAN: 1 series; alignmentPeriod=300s: 1 point per execution
//   - Execution period is always 30s for threshold conditions, regardless of
//     alignmentPeriod: 1 pt/exec × 86,400 exec/mo × $0.50/1M = $0.0432
//   - Total = $0.3932
func TestThresholdAlertExpectedCost(t *testing.T) {
	refCost := monthlyMetricReferenceCost(1)
	ptsCost := monthlyPointsCost(1, defaultExecutionPeriodSec)
	total := refCost + ptsCost
	if total != 0.3932 {
		t.Fatalf("total cost = %v, want 0.3932", total)
	}
}
