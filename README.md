## Instascale controller

# High Level Steps
- User submits Multi GPU job(s)
- Job(s) lands in MCAD queue
- When resources are not available it triggers scaling i.e. calls instascale
- Instascale looks at resource requests specified by the user and matches those with the desired Machineset(s) to get nodes.
- After instascaling, when aggregate resources are available to run the job MCAD dispatches the job.
- When job completes, resources obtained for the job are released.

# Usage
- To build locally : `make run`
- To build and release a docker image for controller : `make IMG=quay.io/project-codeflare/instascale:<TAG> docker-build docker-push`
- Note that the other contents of the Makefile (as well as the `config` and `bin` dirs) exist for future operator development, and are not currently utilized
- To deploy on kubernetes cluster use the `deployments` dir for now:
```
git clone https://github.com/project-codeflare/instascale.git
cd deployment/
oc apply -f instascale-sa.yaml
oc apply -f instascale-clusterrole.yaml
oc apply -f instascale-clusterrolebinding.yaml
oc apply -f deployment.yaml
```

# Testing

TBD