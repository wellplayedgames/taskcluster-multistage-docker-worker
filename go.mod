module github.com/wellplayedgames/taskcluster-multistage-docker-worker

go 1.16

require (
	github.com/alecthomas/kong v0.2.11
	github.com/cenkalti/backoff/v3 v3.2.2
	github.com/docker/cli v0.0.0-20201002160228-7c0824cf3fa4
	github.com/docker/distribution v2.8.0+incompatible
	github.com/docker/docker v1.13.1
	github.com/docker/docker-credential-helpers v0.6.3 // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-units v0.4.0 // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.2.1
	github.com/go-logr/zapr v0.2.0
	github.com/logrusorgru/aurora/v3 v3.0.0
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/runc v0.1.1 // indirect
	github.com/taskcluster/slugid-go v1.1.0
	github.com/taskcluster/taskcluster/v41 v41.0.1
	github.com/tidwall/gjson v1.6.1
	github.com/wojas/genericr v0.2.0
	go.uber.org/zap v1.10.0
	gotest.tools/v3 v3.0.2 // indirect
)
