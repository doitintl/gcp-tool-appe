# appe - Alerting Policy Price Estimator

Starting April 2025, Google will begin charging for [Alerting Policies](https://cloud.google.com/monitoring/alerts).

While Google has provided [documentation and examples](https://cloud.google.com/stackdriver/pricing#pricing-alerting), it is still very hard to actually estimate the cost of an Alerting Policy, let alone if you have dozens or possible hundreds of them.
Using `appe`, you can easily estimate the price of not just an individual Alerting Policy, but all of them in your entire organization using a single command.

## Quick Start
1. Make sure you have Application Default Credentials set up correctly (See [Authentication](#authentication))
2. Make sure you have the [recommended roles](#recommended-roles)
3. Download the [release](https://github.com/doitintl/gcp-tool-appe/releases) for your operating system and CPU architecture
4. Run `appe` on your project with `appe -p PROJECT_ID` (or see [usage](#usage) for other examples)

## Output
`appe` can output human-readable output to the standard console output (`stdout`) or stream the results to a CSV file while it is scanning with the `--csvOutput FILENAME` flag.

## Required Permissions
In order to get the metadata of a policy or list the existing policies within a project, you will need the following permissions:
- `monitoring.alertPolicies.get`
- `monitoring.alertPolicies.list`

These would be included in the [Monitoring AlertPolicy Viewer](https://cloud.google.com/iam/docs/understanding-roles#monitoring.alertPolicyViewer) (`roles/monitoring.alertPolicyViewer`) role. However, the metadata is not enough to estimate the price and we will need to actually execute the policy’s condition. This requires the `monitoring.timeSeries.list` permission, which is included in the [Monitoring Viewer](https://cloud.google.com/iam/docs/understanding-roles#monitoring.viewer) (`roles/monitoring.viewer`) role.
If you want to run `appe` on more than individual policies, you will also need the `resourcemanager.projects.list` permission (which is also conveniently included in the Monitoring Viewer role). If you need to recursively scan for projects (i.e. go into subfolders), you will also need the `resourcemanager.folders.list` permission.
You can also use the `--testPermissions` flag to let `appe` verify that you have the correct permissions before trying to use them in order to avoid errors in your logs.

## Recommended Roles
We recommend that you assign the following two roles for full compatibility:
- [Monitoring Viewer](https://cloud.google.com/iam/docs/understanding-roles#monitoring.viewer) (`roles/monitoring.viewer`)
- [Browser](https://cloud.google.com/iam/docs/understanding-roles#browser) (`roles/browser`)

## Installation
The easiest way to get and install `appe` is to download one of the pre-compiled binaries from the [releases](https://github.com/doitintl/gcp-tool-appe/releases). `appe` is a self-contained binary without any dependencies and can be run from anywhere. You do not need to download any runtime and there is no need for an installer.

See also https://cloud.google.com/docs/authentication/provide-credentials-adc#local-dev.

### macOS
Since the binary is not signed with an Apple Developer Certificate, your Mac will likely report it as untrustworthy.
There are two ways to deal with this:
1. Run `xattr -dr com.apple.quarantine <path to appe>` to add the executable to the allow list (credit to [openecoacoustics.org](https://openecoacoustics.org/resources/help-centre/software/unsigned/))
2. Compile it yourself (see below)

### Compile appe Yourself
The easiest way to do this is to:
1. [Install Go](https://go.dev/doc/install)
2. Run `go install github.com/doitintl/gcp-tool-appe@latest`

This will download the dependencies, compile the program and put the executable in your `$GOPATH/bin` directory (see [GOPATH](https://go.dev/wiki/GOPATH))

Alternatively, you can also clone the repository and run `go build` to compile it manually.

## Authentication
`appe` uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) (ADC) to authenticate against the various APIs.
If you are running `appe` on a Google Cloud compute service such as Compute Engine, it will use the service's Service Account to authenticate.

If you are running `appe` locally, the easiest way to set up ADC is to use [gcloud](https://cloud.google.com/sdk/gcloud/reference/auth/application-default/login) by running:
```bash
gcloud auth application-default login
```

## Usage
Using `appe` is fairly straightforward

### Estimate the Price of Individual Policies
To estimate the price for individual policies, you can reference them directly with the `--policy` flag:
```bash
./appe --policy projects/PROJECT_ID/alertPolicies/POLICY_ID
```
You can also specify multiple policies:
```bash
./appe --policy projects/PROJECT_ID/alertPolicies/POLICY_ID_1,projects/PROJECT_ID/alertPolicies/POLICY_ID_2
```

### Estimate the Price for all Policies in a Project
To estimate the price for all policies in a project, you can specify the project either with the `--project` flag or the shorthand `-p`:
```bash
./appe -p PROJECT_ID
```
You can also specify multiple projects:
```bash
./appe -p PROJECT_ID_1,PROJECT_ID_2
```

### Estimate the Price for all Policies in all Projects in a Folder
To estimate the price of all policies in all projects in a folder, you can specify the folder ID either with the `--folder` flag or the shorthand `-f`:
```bash
./appe -f FOLDER_ID
```
You can also specify multiple folders:
```bash
./appe -f FOLDER_ID_1,FOLDER_ID_2
```
Note that you will need to specify the `--recursive` or `-r` flag to also scan subfolders.

### Estimate the Price for all Policies in all Projects in an Organization
To estimate the price of all policies in all projects in an organization, you can specify the organization ID either with the `--organization` flag or the shorthand `-o`:
```bash
./appe -o ORG_ID
```
You can also specify multiple organizations:
```bash
./appe -o ORG_ID_1,ORG_ID_2
```
Note that you will need to specify the `--recursive` or `-r` flag to also scan subfolders.

### Supported Condition Types
The following condition types are supported by `appe`:
- Monitoring Query Language (MQL) via [projects.timeSeries.query](https://cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.timeSeries/query)
- Threshold and Absence via [projects.timeSeries.list](https://cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.timeSeries/list)
- Prometheus Query Language (PromQL / PQL) via [projects.location.prometheus.api.v1.query_range](https://cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.location.prometheus.api.v1/query_range)

### All Flags
```
  -c, --csvOut string           Path to a CSV file to redirect output to. If this is not set, human-readable output will be given on stdout.
  -d, --duration duration       The delta from now to go back in time for query. Default is 30 days. (default 720h0m0s)
  -e, --excludeFolder strings   One or more folders to exclude. Separated by  ",".
  -f, --folder strings          One or more folders to scan. Use the "-r" flag to scan recursively. Separated by ",".
  -h, --help                    help for appe
  -i, --includeDisabled         If the application should also include disabled policies. (default false)
  -o, --organization strings    One or more organizations to scan. Use the "-r" flag to scan recursively. Separated by ",".
      --policy strings          One or more alerting policies to analyze. Names must be given in full in the format "projects/PROJECT_ID/alertPolicies/POLICY_ID". Separated by ",".
  -p, --project strings         One or more projects to scan. Separated by ",".
  -q, --quotaProject string     A quota or billing project. Useful if you don't have the serviceusage.services.use permission in the target project.
  -r, --recursive               If parent should be scanned recursively. If this is not set, only projects at the root of the folder or organization will be scanned. (default false)
  -t, --testPermissions         If the application should verify that the user has the necessary permissions before processing a project. (default false)
      --threads int             Number of threads to use to process folders, projects and policies in parallel. (default 4)
  -v, --version                 version for appe
```
