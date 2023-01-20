# InstaScale

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

