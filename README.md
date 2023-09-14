# <img src=https://user-images.githubusercontent.com/10506868/216672268-12630923-d9a4-4298-82d3-d07d28b8a234.png alt=InstaScale width=373 height=146 title=InstaScale>

[![Go](https://github.com/project-codeflare/instascale/actions/workflows/go.yml/badge.svg?branch=main)](https://github.com/project-codeflare/instascale/actions/workflows/go.yml)

InstaScale is a controller that works with Multi-cluster-app-dispatcher (MCAD) to get aggregated resources available in the kubernetes cluster without creating pending pods. It uses [machinesets](https://github.com/openshift/machine-api-operator) to launch instances on cloud provider to be added to the Kubernetes cluster.

Key features:
- Acquires aggregated heterogenous instances needed for workload execution.
- Does not clog Kubernetes control plane.
- Works with your Kubernetes scheduling system to schedule pods on aggregated resources.
- Terminates instances on workload completion.


# InstaScale and MCAD interaction
- User submits Multi GPU job(s)
- Job(s) lands in MCAD queue
- When resources are not available it triggers scaling i.e. calls InstaScale
- InstaScale looks at resource requests specified by the user and matches those with the desired Machineset(s) to get nodes.
- After InstaScal-ing, when aggregate resources are available to run the job MCAD dispatches the job.
- When job completes, resources obtained for the job are released.

# Development

## Pre-requisites
-  Installed Go version 1.19
-  Running OpenShift cluster

## Building
- To build locally : `make build`
- To run locally : `make run`
## Image creation
- To build and release a docker image for controller : `make IMG=quay.io/project-codeflare/instascale:<TAG> image-build image-push`
- Note that the other contents of the Makefile (as well as the `config` and `bin` dirs) exist for future operator development, and are not currently utilized
## Deployment
- Deploy InstaScale using: `make deploy`

## Running an InstaScale deployment locally with Visual Studio Code
- Deploy MCAD using steps [here](https://github.com/project-codeflare/multi-cluster-app-dispatcher/blob/main/doc/deploy/deployment.md).

- In Visual Studio Code update `.vscode/launch.json` so that `"KUBECONFIG"` points to your Kubernetes config file.<br>
- If you changed the namespace in `config/default/kustomization.yaml` update the `args[]` in `launch.json` to include `"--configs-namespace=<YOUR_NAMESPACE>", "--ocm-secret-namespace=<YOUR_NAMESPACE>"`.<br>
- You can now run the local deployment with the debugger.
## Running locally with a OSD cluster
Running InstaScale locally to an OSD cluster requires extra steps from the above.
- Add the `instascale-ocm-secret` 
    - Get your API token from [here](https://console.redhat.com/openshift/token)
    - Navigate to Workloads -> secrets
    - Select your project to `instascale-system`
    - Click Create -> Key/value secret
    - Secret name: `instascale-ocm-secret`
    - Key: `token`
    - Value: `<YOUR_API_TOKEN>`
    - Click Create
## Testing

Run tests with command: 
``` 
go test -v ./controllers/

```

## Release process


Prerequisite:
- Build and release [MCAD](https://github.com/project-codeflare/multi-cluster-app-dispatcher)
- Make sure that MCAD version is published on [Go package site](https://pkg.go.dev/github.com/project-codeflare/multi-cluster-app-dispatcher?tab=versions)


1. Run [instascale-release.yml](https://github.com/project-codeflare/instascale/actions/workflows/instascale-release.yml) action.
2. Verify that [instascale-release.yml](https://github.com/project-codeflare/instascale/actions/workflows/instascale-release.yml) action passed successfully.

