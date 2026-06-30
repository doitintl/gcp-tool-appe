package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/iam/apiv1/iampb"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"google.golang.org/api/iterator"
	monitoring_v1 "google.golang.org/api/monitoring/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type policy struct {
	TimeSeries    int
	PointsPerExec int
	Conditions    int
	ProjectId     string
	Name          string
	DisplayName   string
	Error         string
	Price         float64
}

type pqlResponse struct {
	Data struct {
		Result []struct {
			Value  []any   `json:"value"`
			Values [][]any `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func listProjects(ctx context.Context, projectsClient *resourcemanager.ProjectsClient, foldersClient *resourcemanager.FoldersClient, parent string, projects chan string, recursive bool, excludedFolders []string) {
	if slices.Contains(excludedFolders, parent[strings.Index(parent, "/")+1:]) {
		return
	}
	itProjects := projectsClient.ListProjects(ctx, &resourcemanagerpb.ListProjectsRequest{
		Parent: parent,
	})
	for {
		project, err := itProjects.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Printf("Failed to list projects under %s: %v\n", parent, err)
			break
		}
		projects <- project.ProjectId
	}
	if recursive {
		itFolders := foldersClient.ListFolders(ctx, &resourcemanagerpb.ListFoldersRequest{
			Parent: parent,
		})
		for {
			folder, err := itFolders.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				log.Printf("Failed to list folders under %s: %v\n", parent, err)
				break
			}
			listProjects(ctx, projectsClient, foldersClient, folder.Name, projects, recursive, excludedFolders)
		}
	}
}

func getProjectId(alertPolicy *monitoringpb.AlertPolicy) string {
	name := alertPolicy.GetName()
	s1 := name[strings.Index(name, "/")+1:]
	return s1[:strings.Index(s1, "/")]
}

func verifyProjectPermissions(ctx context.Context, projectsClient *resourcemanager.ProjectsClient, projectId string, projectsTested chan string, testPermissions bool) {
	if testPermissions {
		permissions := []string{"monitoring.timeSeries.list", "monitoring.alertPolicies.get", "monitoring.alertPolicies.list"}
		resp, err := projectsClient.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
			Resource:    "projects/" + projectId,
			Permissions: permissions,
		})
		if err != nil {
			log.Printf("Failed to test IAM permissions on project %s: %v\n", projectId, err)
			return
		}
		for i := range permissions {
			if !slices.Contains(resp.GetPermissions(), permissions[i]) {
				log.Printf("No permission %s on %s. Skipping\n", permissions[i], projectId)
				return
			}
		}
	}
	projectsTested <- projectId
}

func listAlertPolicies(ctx context.Context, projectId string, includeDisabled bool, alertingPolicyClient *monitoring.AlertPolicyClient, policiesIn chan *monitoringpb.AlertPolicy) {
	alertPoliciesIt := alertingPolicyClient.ListAlertPolicies(ctx, &monitoringpb.ListAlertPoliciesRequest{
		Name: "projects/" + projectId,
	})
	for {
		alertPolicy, err := alertPoliciesIt.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Printf("Failed to list policies in %s: %v\n", projectId, err)
			break
		}
		enabled := alertPolicy.GetEnabled()
		if (enabled != nil && enabled.GetValue()) || includeDisabled {
			policiesIn <- alertPolicy
		}
	}
}

// alignmentWindowSeconds returns the size (in seconds) of the query window that
// one alert execution sees. This is the alignment period when one is set, or the
// default execution period otherwise.
func alignmentWindowSeconds(aggregations []*monitoringpb.Aggregation) int64 {
	for _, agg := range aggregations {
		if ap := agg.GetAlignmentPeriod(); ap != nil && ap.Seconds > 0 {
			return ap.Seconds
		}
	}
	return defaultExecutionPeriodSec
}

// countPointsFromTimeSeries queries a FULL time series response for one execution
// window and returns the total number of data points across all series. This
// reflects what Google bills: each returned point in each execution counts as one
// billable point.
func countPointsFromTimeSeries(ctx context.Context, metricClient *monitoring.MetricClient, req *monitoringpb.ListTimeSeriesRequest) (timeSeries int, points int) {
	req.View = monitoringpb.ListTimeSeriesRequest_FULL
	it := metricClient.ListTimeSeries(ctx, req)
	for {
		ts, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		timeSeries++
		points += len(ts.Points)
	}
	return timeSeries, points
}

// countPointsFromMQL approximates "one execution" of an MQL condition: for
// each series the query currently returns, it counts exactly one billable
// point — the one a fresh execution at windowSec's cadence would have
// produced — but it tolerates metric ingestion lag by querying a wider
// trailing window (see mqlIngestionLookbackSec) and keeping only the most
// recent resampled point per series, rather than requiring data in the
// literal last windowSec.
//
// windowSec should be the evaluation interval parsed from the query's own
// "| every X" clause (see mqlEvaluationIntervalSeconds), since that is what
// actually determines how often Cloud Monitoring (re-)evaluates the
// condition and resamples its output.
func countPointsFromMQL(ctx context.Context, queryClient *monitoring.QueryClient, name string, query string, windowSec int64) (timeSeries int, points int) {
	lookbackSec := int64(mqlIngestionLookbackSec)
	if windowSec > lookbackSec {
		lookbackSec = windowSec
	}
	mqlWithWindow := fmt.Sprintf("%s\n| within %ds", strings.TrimSpace(query), lookbackSec)
	it := queryClient.QueryTimeSeries(ctx, &monitoringpb.QueryTimeSeriesRequest{
		Name:  name,
		Query: mqlWithWindow,
	})
	for {
		ts, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		// Count only the most recently resampled point per series — what a
		// single execution at `windowSec` cadence would have returned, had
		// the metric not lagged.
		if len(ts.GetPointData()) > 0 {
			timeSeries++
			points++
		}
	}
	return timeSeries, points
}

// mqlIngestionLookbackSec mirrors promqlIngestionLookbackSec: Cloud
// Monitoring metrics can lag a few minutes behind wall-clock time, so an
// MQL "| within" window scoped to the literal evaluation interval frequently
// (and incorrectly) returns zero results even for metrics that are actively
// reporting.
const mqlIngestionLookbackSec = 5 * 60

// countPointsFromPromQL counts the billable points for one execution of a
// PromQL condition.
//
// Google bills a PromQL condition by splitting it into subqueries on every
// top-level logical operator (and/or/unless) and comparison operator (>, <,
// >=, <=, ==, !=), then summing the points returned by each subquery
// (https://cloud.google.com/products/observability/pricing#pricing-alerting).
// Evaluating the full expression (with its trailing comparison) instead would
// undercount: PromQL comparisons drop non-matching series, but Google still
// bills the points the underlying metric returned before that comparison was
// applied.
func countPointsFromPromQL(svc *monitoring_v1.Service, name string, query string, execPeriodSec int64, end *timestamppb.Timestamp) (timeSeries int, points int, err error) {
	for _, subquery := range billablePromQLSubqueries(query) {
		ts, pts, qErr := evalPromQLInstant(svc, name, subquery, execPeriodSec, end)
		if qErr != nil {
			return timeSeries, points, qErr
		}
		timeSeries += ts
		points += pts
	}
	return timeSeries, points, nil
}

// promqlIngestionLookbackSec is how far back evalPromQLInstant searches for
// the most recent sample of each series. Cloud Monitoring metrics can lag a
// few minutes behind wall-clock time, so a literal instant query at "now"
// frequently (and incorrectly) returns zero results even for metrics that are
// actively reporting.
const promqlIngestionLookbackSec = 5 * 60

// evalPromQLInstant approximates "one execution" of a PromQL (sub)query: for
// each series the query currently returns, it counts exactly one billable
// point (matching PromQL instant-query semantics), but it tolerates metric
// ingestion lag by querying a short trailing window and keeping only the
// latest sample per series, rather than requiring data at the literal instant
// `end`.
func evalPromQLInstant(svc *monitoring_v1.Service, name string, query string, execPeriodSec int64, end *timestamppb.Timestamp) (timeSeries int, points int, err error) {
	lookbackSec := int64(promqlIngestionLookbackSec)
	if execPeriodSec > lookbackSec {
		lookbackSec = execPeriodSec
	}
	rangeResp, rangeErr := svc.Projects.Location.Prometheus.Api.V1.QueryRange(name, "global", &monitoring_v1.QueryRangeRequest{
		Query: query,
		Start: end.AsTime().Add(-time.Duration(lookbackSec) * time.Second).Format(time.RFC3339),
		End:   end.AsTime().Format(time.RFC3339),
		Step:  fmt.Sprintf("%ds", execPeriodSec),
	}).Do()
	if rangeErr != nil {
		return 0, 0, rangeErr
	}
	j, jErr := rangeResp.MarshalJSON()
	if jErr != nil {
		return 0, 0, jErr
	}
	pqlResp := &pqlResponse{}
	if uErr := json.Unmarshal(j, pqlResp); uErr != nil {
		return 0, 0, uErr
	}
	// Count only the most recent sample per series — this is what an instant
	// query at `end` would have returned, had the metric not lagged.
	seriesWithData := 0
	for _, r := range pqlResp.Data.Result {
		if len(r.Values) > 0 {
			seriesWithData++
		}
	}
	return seriesWithData, seriesWithData, nil
}

func processAlertPolicy(
	ctx context.Context,
	queryClient *monitoring.QueryClient,
	metricClient *monitoring.MetricClient,
	monitoring_v1Service *monitoring_v1.Service,
	alertPolicy *monitoringpb.AlertPolicy,
	start *timestamppb.Timestamp,
	end *timestamppb.Timestamp,
	policiesOut chan *policy) {
	projectId := getProjectId(alertPolicy)
	name := "projects/" + projectId
	conditions := alertPolicy.GetConditions()
	policyOut := &policy{
		ProjectId:   projectId,
		Name:        alertPolicy.GetName(),
		DisplayName: alertPolicy.GetDisplayName(),
		Conditions:  len(conditions),
	}

	for i := range conditions {
		mql := conditions[i].GetConditionMonitoringQueryLanguage()
		pql := conditions[i].GetConditionPrometheusQueryLanguage()
		threshold := conditions[i].GetConditionThreshold()
		absent := conditions[i].GetConditionAbsent()

		if mql != nil {
			policyOut.Price += monthlyMetricReferenceCost(countMetricReferencesInQuery(mql.GetQuery()))

			// MQL's own "| every X" clause only controls how the query
			// resamples data internally — it does not change how often Cloud
			// Monitoring executes the condition for billing purposes. Like
			// threshold/absence, MQL conditions always bill on the fixed 30s
			// execution period; only PromQL's evaluationInterval can change it.
			// https://cloud.google.com/products/observability/pricing#pricing-alerting
			//
			// We still pass the "| every X" interval down to size the query
			// window: it ensures we look back far enough to catch at least
			// one resampled bucket when "every" is large (e.g. "every 1h").
			mqlIntervalSec := mqlEvaluationIntervalSeconds(mql.GetQuery())
			ts, pts := countPointsFromMQL(ctx, queryClient, name, mql.GetQuery(), mqlIntervalSec)
			policyOut.TimeSeries += ts
			policyOut.PointsPerExec += pts
			policyOut.Price += monthlyPointsCost(pts, defaultExecutionPeriodSec)
		}

		if pql != nil {
			execPeriodSec := executionPeriodSeconds(pql.GetEvaluationInterval().GetSeconds())
			policyOut.Price += monthlyMetricReferenceCost(countMetricReferencesInQuery(pql.GetQuery()))

			ts, pts, err := countPointsFromPromQL(monitoring_v1Service, name, pql.GetQuery(), execPeriodSec, end)
			if err != nil {
				policyOut.Error = err.Error()
				continue
			}
			policyOut.TimeSeries += ts
			policyOut.PointsPerExec += pts
			policyOut.Price += monthlyPointsCost(pts, execPeriodSec)
		}

		if threshold != nil || absent != nil {
			policyOut.Price += monthlyMetricReferenceCost(1)

			var aggregations []*monitoringpb.Aggregation
			var filter string
			if threshold != nil {
				filter = threshold.GetFilter()
				aggregations = threshold.GetAggregations()
			} else {
				filter = absent.GetFilter()
				aggregations = absent.GetAggregations()
			}

			// One execution window = the alignment period (or default execution period).
			windowSec := alignmentWindowSeconds(aggregations)
			windowStart := timestamppb.New(end.AsTime().Add(-time.Duration(windowSec) * time.Second))

			tsReq := &monitoringpb.ListTimeSeriesRequest{
				Name:   name,
				Filter: filter,
				Interval: &monitoringpb.TimeInterval{
					EndTime:   end,
					StartTime: windowStart,
				},
			}
			if len(aggregations) > 0 {
				tsReq.Aggregation = aggregations[0]
			}
			if len(aggregations) > 1 {
				tsReq.SecondaryAggregation = aggregations[1]
			}

			ts, pts := countPointsFromTimeSeries(ctx, metricClient, tsReq)
			policyOut.TimeSeries += ts
			policyOut.PointsPerExec += pts
			// Billing always uses the fixed 30s execution period for
			// non-PromQL conditions, regardless of alignmentPeriod — only
			// PromQL's evaluationInterval can change the execution period.
			// https://cloud.google.com/products/observability/pricing#pricing-alerting
			policyOut.Price += monthlyPointsCost(pts, defaultExecutionPeriodSec)
		}
	}

	policiesOut <- policyOut
}
