package main

import (
	"fmt"
	"github.com/docker/docker/client"
	"io/ioutil"

	"github.com/ghodss/yaml"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/worker"
)

type Run struct {
	Config string `help:"Path to a configuration file to load."`
}

func (r *Run) Run(c *commandContext) error {
	// Load config
	config := worker.DefaultConfig
	if err := config.ParseEnv(); err != nil {
		return err
	}

	if r.Config != "" {
		contents, err := ioutil.ReadFile(r.Config)
		if err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}

		err = yaml.Unmarshal(contents, &config)
		if err != nil {
			return fmt.Errorf("error parsing config: %w", err)
		}
	}

	docker, err := client.NewEnvClient()
	if err != nil {
		return err
	}

	w, err := worker.NewWorker(c.Logger, &config, docker)
	if err != nil {
		return err
	}

	return w.Run(c.Context, c.StopCh)
}
