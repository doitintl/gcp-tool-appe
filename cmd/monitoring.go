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
	TimeSeries  int
	Conditions  int
	ProjectId   string
	Name        string
	DisplayName string
	Error       string
	Price       float64
}

type pqlResponse struct {
	Data struct {
		Result []struct {
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
		Price:       1.5 * float64(len(conditions)),
	}
	for i := range conditions {
		mql := conditions[i].GetConditionMonitoringQueryLanguage()
		pql := conditions[i].GetConditionPrometheusQueryLanguage()
		threshold := conditions[i].GetConditionThreshold()
		absent := conditions[i].GetConditionAbsent()
		if mql != nil {
			tsIt := queryClient.QueryTimeSeries(ctx, &monitoringpb.QueryTimeSeriesRequest{
				Name:  name,
				Query: mql.GetQuery(),
			})
			for {
				_, err := tsIt.Next()
				if err == iterator.Done {
					break
				}
				if err != nil {
					policyOut.Error = err.Error()
					break
				}
				// 60 (seconds) * 60 (minutes) * 24 (hours) * 30 (days) / 30 (step) * 0.35 (price) / 1000000 (per 1M) = 0.03024 (price per time series)
				policyOut.Price += 0.03024
				policyOut.TimeSeries++
			}
		}
		if pql != nil {
			seconds := pql.GetEvaluationInterval().GetSeconds()
			resp, err := monitoring_v1Service.Projects.Location.Prometheus.Api.V1.QueryRange(name, "global", &monitoring_v1.QueryRangeRequest{
				Query: pql.GetQuery(),
				Start: start.AsTime().Format(time.RFC3339),
				End:   end.AsTime().Format(time.RFC3339),
				Step:  fmt.Sprintf("%ds", seconds),
			}).Do()
			if err != nil {
				policyOut.Error = err.Error()
				continue
			}
			j, err := resp.MarshalJSON()
			if err != nil {
				policyOut.Error = err.Error()
				continue
			}
			pqlResp := &pqlResponse{}
			err = json.Unmarshal(j, pqlResp)
			if err != nil {
				policyOut.Error = err.Error()
				continue
			}
			// 60 (seconds) * 60 (minutes) * 24 (hours) * 30 (days) = 2592000
			// 2592000 * 0.35 (price) / 1000000 (per 1M) =
			// 0.9072 / step * time series = price for all time series with this condition
			policyOut.Price += 0.9072 / float64(seconds) * float64(len(pqlResp.Data.Result))
			policyOut.TimeSeries += len(pqlResp.Data.Result)
		}
		if threshold != nil || absent != nil {
			tsReq := &monitoringpb.ListTimeSeriesRequest{
				Name: name,
				View: monitoringpb.ListTimeSeriesRequest_HEADERS,
				Interval: &monitoringpb.TimeInterval{
					EndTime:   end,
					StartTime: start,
				},
			}
			aggregations := []*monitoringpb.Aggregation{}
			if threshold != nil {
				tsReq.Filter = threshold.GetFilter()
				aggregations = threshold.GetAggregations()
			}
			if absent != nil {
				tsReq.Filter = absent.GetFilter()
				aggregations = absent.GetAggregations()
			}
			if len(aggregations) > 0 {
				tsReq.Aggregation = aggregations[0]
			}
			if len(aggregations) > 1 {
				tsReq.SecondaryAggregation = aggregations[1]
			}
			if tsReq.Aggregation == nil || tsReq.Aggregation.GetCrossSeriesReducer().String() == "REDUCE_COUNT_FALSE" || tsReq.SecondaryAggregation.GetCrossSeriesReducer().String() == "REDUCE_COUNT_FALSE" {
				tsReq.View = monitoringpb.ListTimeSeriesRequest_FULL
			}
			tsIt := metricClient.ListTimeSeries(ctx, tsReq)
			for {
				_, err := tsIt.Next()
				if err == iterator.Done {
					break
				}
				if err != nil {
					policyOut.Error = err.Error()
					break
				}
				// 60 (seconds) * 60 (minutes) * 24 (hours) * 30 (days) / 30 (step) * 0.35 (price) / 1000000 (per 1M) = 0.03024 (price per time series)
				policyOut.Price += 0.03024
				policyOut.TimeSeries++
			}
		}
	}
	policiesOut <- policyOut
}
