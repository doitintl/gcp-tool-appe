package cmd

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"github.com/spf13/cobra"
	monitoring_v1 "google.golang.org/api/monitoring/v1"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Version: "0.2",
	Use:     "appe",
	Short:   "Alerting Policy Price Estimator",
	Long:    `Scans for alerting policies in the specified projects, folder or orgs and approximates their cost by executing the queries defined in them against the monitoring API`,
	Run:     func(cmd *cobra.Command, args []string) {},
	Example: `To estimate the price for individual policies, you can reference them directly with the --policy flag:
./appe --policy projects/PROJECT_ID/alertPolicies/POLICY_ID
You can also specify multiple policies:
./appe --policy projects/PROJECT_ID/alertPolicies/POLICY_ID_1,projects/PROJECT_ID/alertPolicies/POLICY_ID_2

To estimate the price for all policies in a project, you can specify the project either with the --project flag or the shorthand -p:
./appe -p PROJECT_ID
You can also specify multiple projects:
./appe -p PROJECT_ID_1,PROJECT_ID_2

./appe -f FOLDER_ID
You can also specify multiple folders:
./appe -f FOLDER_ID_1,FOLDER_ID_2

To estimate the price of all policies in all projects in an organization, you can specify the organization ID either with the --organization flag or the shorthand -o:
./appe -o ORG_ID
You can also specify multiple organizations:
./appe -o ORG_ID_1,ORG_ID_2

Note that you will need to specify the --recursive or -r flag to also scan subfolders.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}

	// Parse flags
	projects, err := rootCmd.Flags().GetStringSlice("project")
	if err != nil {
		log.Fatalln(err)
	}
	folders, err := rootCmd.Flags().GetStringSlice("folder")
	if err != nil {
		log.Fatalln(err)
	}
	organizations, err := rootCmd.Flags().GetStringSlice("organization")
	if err != nil {
		log.Fatalln(err)
	}
	csvOut, err := rootCmd.Flags().GetString("csvOut")
	if err != nil {
		log.Fatalln(err)
	}
	threads, err := rootCmd.Flags().GetInt64("threads")
	if err != nil {
		log.Fatalln(err)
	}
	recursive, err := rootCmd.Flags().GetBool("recursive")
	if err != nil {
		log.Fatalln(err)
	}
	testPermissions, err := rootCmd.Flags().GetBool("testPermissions")
	if err != nil {
		log.Fatalln(err)
	}
	includeDisabled, err := rootCmd.Flags().GetBool("includeDisabled")
	if err != nil {
		log.Fatalln(err)
	}
	summary, err := rootCmd.Flags().GetBool("summary")
	if err != nil {
		log.Fatalln(err)
	}
	quotaProject, err := rootCmd.Flags().GetString("quotaProject")
	if err != nil {
		log.Fatalln(err)
	}
	duration, err := rootCmd.Flags().GetDuration("duration")
	if err != nil {
		log.Fatalln(err)
	}
	excludedFolders, err := rootCmd.Flags().GetStringSlice("excludeFolder")
	if err != nil {
		log.Fatalln(err)
	}
	policies, err := rootCmd.Flags().GetStringSlice("policy")
	if err != nil {
		log.Fatalln(err)
	}

	// Set up re-usable variables
	ctx := context.Background()
	now := time.Now()
	end := timestamppb.Now()
	start := timestamppb.New(now.Add(-duration))
	projectsIn := make(chan string, threads)
	projectsTested := make(chan string, threads)
	policiesIn := make(chan *monitoringpb.AlertPolicy, threads)
	policiesOut := make(chan *policy, threads)
	lenP := len(projects)
	lenF := len(folders)
	lenO := len(organizations)
	lenPol := len(policies)

	// Set up API clients
	alertingPolicyClient, err := monitoring.NewAlertPolicyClient(ctx, option.WithQuotaProject(quotaProject))
	if err != nil {
		log.Fatalf("Failed to create alert policy client: %v", err)
	}
	queryClient, err := monitoring.NewQueryClient(ctx, option.WithQuotaProject(quotaProject))
	if err != nil {
		log.Fatalf("Failed to create query client: %v", err)
	}
	metricClient, err := monitoring.NewMetricClient(ctx, option.WithQuotaProject(quotaProject))
	if err != nil {
		log.Fatalf("Failed to create metric client: %v", err)
	}
	projectsClient, err := resourcemanager.NewProjectsClient(ctx, option.WithQuotaProject(quotaProject))
	if err != nil {
		log.Fatalf("Failed to create projects client: %v", err)
	}
	foldersClient, err := resourcemanager.NewFoldersClient(ctx, option.WithQuotaProject(quotaProject))
	if err != nil {
		log.Fatalf("Failed to create folders client: %v", err)
	}
	monitoring_v1Service, err := monitoring_v1.NewService(ctx, option.WithQuotaProject(quotaProject))
	if err != nil {
		log.Fatalf("Failed to create monitoring v1 client: %v", err)
	}

	// If the application was executed with the --project or -p flag, put all the projects directly in the projects channel.
	// Once done, we close the projects channel because we know there won't be any more projects coming in.
	if lenP > 0 {
		if lenP > int(threads) {
			threads = int64(lenP)
		}
		go func() {
			for i := range projects {
				projectsIn <- projects[i]
			}
			close(projectsIn)
		}()
	}

	// If the application was executed with orgs or folders, we first list the parents under them.
	// Once done, we close the projects channel because we know there won't be any more projects coming in.
	if lenF > 0 {
		go func() {
			for i := range folders {
				listProjects(ctx, projectsClient, foldersClient, "folders/"+folders[i], projectsIn, recursive, excludedFolders)
			}
			close(projectsIn)
		}()
	}
	if lenO > 0 {
		go func() {
			for i := range organizations {
				listProjects(ctx, projectsClient, foldersClient, "organizations/"+organizations[i], projectsIn, recursive, excludedFolders)
			}
			close(projectsIn)
		}()
	}

	// If one or more individual policies should be analyzed, we need to first get them from the API.
	// We then put them directly on the policiesIn channel, which will be processes by threads that are spawned below.
	// Finally, we will close the projectsIn channel once done, because the policiesIn channel will be closed automatically.
	if lenPol > 0 {
		if lenPol > int(threads) {
			threads = int64(lenPol)
		}
		go func() {
			for i := range policies {
				policy, err := alertingPolicyClient.GetAlertPolicy(ctx, &monitoringpb.GetAlertPolicyRequest{
					Name: policies[i],
				})
				if err != nil {
					log.Fatal(err)
				}
				policiesIn <- policy
			}
			close(projectsIn)
		}()
	}

	// We create a wait group with the number of threads to use for parallel processing of projects
	// We then spawn the threads that will verify the permissions on the projects and put them in the projectsTested channel
	var wg1 sync.WaitGroup
	wg1.Add(int(threads))
	for i := 0; i < int(threads); i++ {
		go func() {
			for project := range projectsIn {
				verifyProjectPermissions(ctx, projectsClient, project, projectsTested, testPermissions)
			}
			wg1.Done()
		}()
	}

	// We create a second wait group with the number of threads to use for parallel processing of projects
	// We then create the threads that will look for policies in the tested projects and put them in the policiesIn channel
	var wg2 sync.WaitGroup
	wg2.Add(int(threads))
	for i := 0; i < int(threads); i++ {
		go func() {
			for project := range projectsTested {
				// processAlertingPolicies(ctx, alertingPolicyClient, queryClient, metricClient, httpClient, project, start, end, parallelPolicies, policiesOut)
				listAlertPolicies(ctx, project, includeDisabled, alertingPolicyClient, policiesIn)
			}
			wg2.Done()
		}()
	}

	// These threads will loop over the found policies and execute their queries to estimate their cost
	var wg3 sync.WaitGroup
	wg3.Add(int(threads))
	for i := 0; i < int(threads); i++ {
		go func() {
			for policy := range policiesIn {
				processAlertPolicy(ctx, queryClient, metricClient, monitoring_v1Service, policy, start, end, policiesOut)
			}
			wg3.Done()
		}()
	}

	// We create one thread that will just wait for the other threads and close the channels in the correct order
	go func() {
		// We wait until all of the threads that may put projects in the projectsTested channel are done before closing it
		wg1.Wait()
		close(projectsTested)
		// We then wait until all of the threads that are listing policies are done before closing the policiesIn channel
		wg2.Wait()
		close(policiesIn)
		// We then wait until all of the threads that are processing policies are done before closing the policiesOut channel
		wg3.Wait()
		close(policiesOut)
	}()

	// If the --csvOut flag was used, we create a CSV writer and write each policy as a line to the file
	if csvOut != "" {
		csvFile, err := os.Create(csvOut)
		if err != nil {
			log.Fatalf("Failed to create CSV file: %v", err)
		}
		defer csvFile.Close()
		csvWriter := csv.NewWriter(csvFile)
		err = csvWriter.Write([]string{"ProjectId", "Policy Name", "Link", "DisplayName", "Conditions", "Time Series", "Price", "Error"})
		if err != nil {
			log.Fatalln("Failed writing header to file", err)
		}
		csvWriter.Flush()
		for policy := range policiesOut {
			err = csvWriter.Write([]string{policy.ProjectId, policy.Name, fmt.Sprintf("https://console.cloud.google.com/monitoring/alerting/policies/%s?project=%s", policy.Name[strings.LastIndex(policy.Name, "/")+1:], policy.ProjectId), policy.DisplayName, strconv.Itoa(policy.Conditions), strconv.Itoa(policy.TimeSeries), strconv.FormatFloat(policy.Price, 'f', 2, 64), policy.Error})
			if err != nil {
				log.Fatalln("Failed writing record to file", err)
			}
			csvWriter.Flush()
		}
		// Otherwise, the application will just output to stdout
	} else if summary {
		policiesSum := 0
		conditionsSum := 0
		timeSeriesSum := 0
		priceSum := 0.0
		for policy := range policiesOut {
			policiesSum++
			conditionsSum += policy.Conditions
			timeSeriesSum += policy.TimeSeries
			priceSum += policy.Price
		}
		log.Printf("Summary: You have %d policies with a combined total of %d conditions and %d time series. It will cost approximately $%f\n", policiesSum, conditionsSum, timeSeriesSum, priceSum)
	} else {
		for policy := range policiesOut {
			log.Printf("Alerting Policy %s (%s) has %d condition(s) and %d time series. It will cost approximately $%f\n", policy.DisplayName, policy.Name, policy.Conditions, policy.TimeSeries, policy.Price)
		}
	}
}

func init() {
	rootCmd.Flags().StringP("quotaProject", "q", "", "A quota or billing project. Useful if you don't have the serviceusage.services.use permission in the target project.")
	rootCmd.Flags().StringP("csvOut", "c", "", "Path to a CSV file to redirect output to. If this is not set, human-readable output will be given on stdout.")
	rootCmd.Flags().StringSlice("policy", nil, "One or more alerting policies to analyze. Names must be given in full in the format \"projects/PROJECT_ID/alertPolicies/POLICY_ID\". Separated by \",\".")
	rootCmd.Flags().StringSliceP("project", "p", nil, "One or more projects to scan. Separated by \",\".")
	rootCmd.Flags().StringSliceP("folder", "f", nil, "One or more folders to scan. Use the \"-r\" flag to scan recursively. Separated by \",\".")
	rootCmd.Flags().StringSliceP("organization", "o", nil, "One or more organizations to scan. Use the \"-r\" flag to scan recursively. Separated by \",\".")
	rootCmd.Flags().StringSliceP("excludeFolder", "e", nil, "One or more folders to exclude. Separated by  \",\".")
	rootCmd.Flags().BoolP("testPermissions", "t", false, "If the application should verify that the user has the necessary permissions before processing a project. (default false)")
	rootCmd.Flags().BoolP("includeDisabled", "i", false, "If the application should also include disabled policies. (default false)")
	rootCmd.Flags().BoolP("summary", "s", false, "Whether the output should just be a summary (sum of all scanned policies) (default false)")
	rootCmd.Flags().BoolP("recursive", "r", false, "If parent should be scanned recursively. If this is not set, only projects at the root of the folder or organization will be scanned. (default false)")
	rootCmd.Flags().Int64("threads", 4, "Number of threads to use to process folders, projects and policies in parallel.")
	rootCmd.Flags().DurationP("duration", "d", 12*time.Hour, "The delta from now to go back in time for query. Default is 12 hours.")
	rootCmd.MarkFlagsOneRequired("policy", "project", "folder", "organization")
	rootCmd.MarkFlagsMutuallyExclusive("policy", "project", "recursive")
	rootCmd.MarkFlagsMutuallyExclusive("policy", "testPermissions")
	rootCmd.MarkFlagsMutuallyExclusive("policy", "includeDisabled")
	rootCmd.MarkFlagsMutuallyExclusive("policy", "project", "excludeFolder")
	rootCmd.MarkFlagsMutuallyExclusive("policy", "project", "folder", "organization")
	rootCmd.MarkFlagsMutuallyExclusive("csvOut", "summary")
}
