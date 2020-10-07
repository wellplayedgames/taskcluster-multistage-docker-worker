// This package contains all of the types which are used throughout the program.
package config

import (
	"github.com/taskcluster/taskcluster/v37/clients/client-go"
)

// Config is the configuration which is used to run this worker.
type Config struct {
	RootURL          string   `json:"rootURL"`
	ClientID         string   `json:"clientId"`
	AccessToken      string   `json:"accessToken"`
	Certificate      string   `json:"certificate,omitempty"`
	AuthorizedScopes []string `json:"authorizedScopes,omitempty"`

	ProvisionerID string `json:"provisionerId"`
	WorkerType    string `json:"workerType"`
	WorkerGroup   string `json:"workerGroup"`
	WorkerID      string `json:"workerId"`

	PublicIP string `json:"publicIp,omitempty"`

	LiveLogPort     int    `json:"liveLogPort,omitempty"`
	LiveLogCertPath string `json:"liveLogCertPath,omitempty"`
	LiveLogKeyPath  string `json:"liveLogCertPath,omitempty"`

	ShutdownOnIdleSeconds *int `json:"shutdownOnIdleSecs,omitempty"`

	DindImage             string `json:"dindImage,omitempty"`
	TaskclusterProxyImage string `json:"taskclusterProxyImage,omitempty"`

	ConcurrentTasks int `json:"concurrentTasks,omitempty"`

	WSTAudience  string `json:"wstAudience,omitempty"`
	WSTServerURL string `json:"wstServerURL,omitempty"`
}

// DefaultConfig is the recommended base configuration for the worker.
var DefaultConfig = Config{
	PublicIP: "localhost",

	LiveLogPort: 13000,

	DindImage:             "docker:dind",
	TaskclusterProxyImage: "taskcluster/taskcluster-proxy:v37.3.0",

	ConcurrentTasks: 1,
}

// ParseEnv fetches config from the environment and merges it in.
func (c *Config) ParseEnv() error {
	return nil
}

// Credentials fetches the credentials from the configuration.
func (c *Config) Credentials() *tcclient.Credentials {
	return &tcclient.Credentials{
		ClientID:         c.ClientID,
		AccessToken:      c.AccessToken,
		Certificate:      c.Certificate,
		AuthorizedScopes: c.AuthorizedScopes,
	}
}
