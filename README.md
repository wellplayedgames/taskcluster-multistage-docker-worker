# Multi-Stage Docker Worker
This project is a spin on the Taskcluster Docker worker. The key difference is
that the Multi-Stage worker supports having multiple steps which are executed
in the same 'pod'. Each pod is run in a dedicated Docker instance (using
docker-in-docker), where they can share volumes without them having to be
transferred over the network.

## Usage
The worker can run regular tasks from Taskcluster with its own payload format.

### Task Example
```yaml
provisionerId: shared
workerType: multistage-docker
created: {$fromNow: ''}
deadline: {$fromNow: '3 hours'}
expires: {$fromNow: '10 days'}
payload:
  steps:
    - image: my/ci/bootstrap
    - image: bitnami/git
      args: ["clone", "https://github.com/my/repo", "."]
    - image: docker
      args: ["build", ".", "-t", "my/image/name"]
    - image: docker
      args: ["push", "my/image/name"]
metadata:
  name: example-task
  description: An **example** task
  owner: name@example.com
```

## Deployment
The worker is compatible with [worker-runner](https://docs.taskcluster.net/docs/reference/workers/worker-runner)
using the `generic-worker` implementation. It has a few configuration variables
but the most common ones match `generic-worker`:

### worker-runner Config Example
```yaml
provider:
  providerType: google  
worker:
  implementation: generic-worker
  path: ./local/multistage-docker-worker
  configPath: local/generic-worker-config.yml
workerConfig:
  wstAudience: taskcluster
  wstServerURL: https://my.taskcluster
  shutdownOnIdleSecs: 30
```

## License
This project is licensed under the Apache 2.0 license.