package main

import (
	"fmt"
	"io/ioutil"
	"os"

	dcfg "github.com/docker/cli/cli/config"
	"github.com/docker/docker/client"
	"github.com/ghodss/yaml"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/config"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/cri"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/log"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/worker"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/workerproto"
)

type Run struct {
	Config           string `help:"Path to a configuration file to load."`
	WithWorkerRunner bool   `help:"Use this flag to enable the worker protocol."`
}

func (r *Run) Run(c *commandContext) error {
	logger := c.Logger
	var gracefulShutdownCh <-chan bool
	requestShutdown := func() error {
		return fmt.Errorf("shutdown requested but not implemented")
	}

	// Configure worker-runner
	if r.WithWorkerRunner {
		communicator := workerproto.NewCommunicator(logger, true)

		baseLogger := logger
		remoteLog := workerproto.AddLogger(communicator, baseLogger)
		logger = log.NewTee(baseLogger, remoteLog)

		gracefulShutdownCh = workerproto.AddGracefulTermination(communicator)
		requestShutdown = workerproto.AddRemoteShutdown(communicator)

		go communicator.Run(os.Stdin, os.Stderr)
	}

	// Load config
	workerConfig := config.DefaultConfig
	if err := workerConfig.ParseEnv(); err != nil {
		return err
	}

	if r.Config != "" {
		contents, err := ioutil.ReadFile(r.Config)
		if err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}

		err = yaml.Unmarshal(contents, &workerConfig)
		if err != nil {
			return fmt.Errorf("error parsing config: %w", err)
		}
	}

	docker, err := client.NewEnvClient()
	if err != nil {
		return err
	}

	dockerConfig := dcfg.LoadDefaultConfigFile(os.Stderr)
	dockerCRI := cri.NewDocker(docker, dockerConfig)
	dind := cri.NewDockerInDockerSandbox(dockerCRI, dockerConfig, workerConfig.DindImage)

	w, err := worker.NewWorker(logger, &workerConfig, dind, gracefulShutdownCh, requestShutdown)
	if err != nil {
		return err
	}

	return w.Run(c.Context, c.StopCh)
}
