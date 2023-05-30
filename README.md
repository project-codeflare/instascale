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
-  Installed go version 1.17
-  Running OpenShift cluster

## Building
- To build locally : `make build`
- To run locally : `make run`
## Image creation
- To build and release a docker image for controller : `make IMG=quay.io/project-codeflare/instascale:<TAG> docker-build docker-push`
- Note that the other contents of the Makefile (as well as the `config` and `bin` dirs) exist for future operator development, and are not currently utilized
## Deployment
- Deploy MCAD using steps [here](https://github.com/project-codeflare/multi-cluster-app-dispatcher/blob/main/doc/deploy/deployment.md).
- Deploy InstaScale using commands below:
```
git clone https://github.com/project-codeflare/instascale.git
cd deployment/
oc apply -f instascale-configmap.yaml
oc apply -f instascale-sa.yaml
oc apply -f instascale-clusterrole.yaml
oc apply -f instascale-clusterrolebinding.yaml
oc apply -f deployment.yaml
```

## Testing

Run tests with command: 
``` 
go test -v ./controllers/

```

## Release process

1. Update [go.mod](https://github.com/project-codeflare/instascale/blob/main/go.mod)/[go.sum](https://github.com/project-codeflare/instascale/blob/main/go.sum) with newest dependency version of multi-cluster-app-dispatcher:

```
go get github.com/project-codeflare/multi-cluster-app-dispatcher
``` 

2. Update version in [VERSION file](https://github.com/project-codeflare/instascale/blob/main/VERSION) to the new release version. Once VERSION file change is merged then [instascale-release.yml](https://github.com/project-codeflare/instascale/actions/workflows/instascale-release.yml) and [go.yml](https://github.com/project-codeflare/instascale/actions/workflows/go.yml) actions are invoked.

> **Note**
> The VERSION file is going to be removed as part of Instascale release process automation.

3. Verify that [instascale-release.yml](https://github.com/project-codeflare/instascale/actions/workflows/instascale-release.yml) action passed successfully.
4. Verify that [go.yml](https://github.com/project-codeflare/instascale/actions/workflows/go.yml) action passed successfully.
5. Create a new release on [release page](https://github.com/project-codeflare/instascale/releases). Provide a tag with same value as in VERSION file formated as semver (with `v` prefix). Provide proper release title and description.

