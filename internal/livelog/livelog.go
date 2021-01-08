// This package implements the livelog transport, optionally using websocktunnel
// or just listening on a port.
package livelog

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/taskcluster/taskcluster/v40/clients/client-go/tcauth"
	"github.com/taskcluster/taskcluster/v40/tools/websocktunnel/client"
	"github.com/taskcluster/taskcluster/v40/tools/websocktunnel/util"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/config"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/pubsubbuffer"
)

// LiveLog is a listener which uses HTTP streaming to deliver up-to-the-second build logs.
type LiveLog struct {
	baseURL  string
	listener net.Listener
	server   *pubsubbuffer.Server
}

// New creates a new LiveLog, given the configuration and a diagnostic logger.
func New(log logr.Logger, config *config.Config) (*LiveLog, error) {
	url, listener, err := createListener(log, config)
	if err != nil {
		return nil, err
	}

	if !strings.HasSuffix(url, "/") {
		url = url + "/"
	}

	log.Info("LiveLog listening", "url", url)

	l := &LiveLog{
		baseURL:  url,
		listener: listener,
		server:   pubsubbuffer.NewServer(),
	}
	return l, nil
}

// BaseURL returns the URL at which the LiveLogs will be hosted.
func (l *LiveLog) BaseURL() string {
	return l.baseURL
}

// Allocate creates a new live log and returns the URL to access it and the
// writer to populate the contents.
func (l *LiveLog) Allocate() (string, pubsubbuffer.WriteSubscribeCloser) {
	path, w := l.server.Allocate()
	url := l.baseURL + path
	return url, w
}

// Serve creates the HTTP server for the LiveLogs and runs it.
func (l *LiveLog) Serve(ctx context.Context) error {
	server := &http.Server{
		Handler: l.server,
	}

	var wg sync.WaitGroup

	go func() {
		<-ctx.Done()
		shutdownCtx, cancelCtx := context.WithTimeout(context.Background(), 10 * time.Second)
		defer cancelCtx()
		server.Shutdown(shutdownCtx)
	}()

	err := server.Serve(l.listener)
	wg.Wait()
	return err
}

func createWSTListener(log logr.Logger, config *config.Config) (string, net.Listener, error) {
	credentials := config.Credentials()
	auth := tcauth.New(credentials, config.RootURL)
	wstClientID := fmt.Sprintf("%s.%s", config.WorkerGroup, config.WorkerID)

	cl, err := client.New(func() (client.Config, error) {
		resp, err := auth.WebsocktunnelToken(config.WSTAudience, wstClientID)
		if err != nil {
			return client.Config{}, err
		}

		config := client.Config{
			ID:         wstClientID,
			TunnelAddr: config.WSTServerURL,
			Token:      resp.Token,
			Retry: client.RetryConfig{
				InitialDelay:        50 * time.Millisecond,
				MaxDelay:            60 * time.Second,
				Multiplier:          1.5,
				RandomizationFactor: 0.2,
			},
			Logger: &tclogr{log},
		}
		return config, nil
	})
	if err != nil {
		return "", nil, err
	}

	return cl.URL(), cl, nil
}

func createListener(log logr.Logger, config *config.Config) (url string, listener net.Listener, err error) {
	if config.WSTServerURL != "" {
		url, listener, err = createWSTListener(log, config)
	} else {
		bind := fmt.Sprintf(":%d", config.LiveLogPort)
		listener, err = net.Listen("tcp", bind)

		protocol := "http"
		if config.LiveLogCertPath != "" {
			protocol = "https"
		}

		url = fmt.Sprintf("%s://%s:%d/", protocol, config.PublicIP, config.LiveLogPort)
	}

	return
}

type tclogr struct {
	logr.Logger
}

var _ util.Logger = (*tclogr)(nil)

func (t *tclogr) Printf(format string, a ...interface{}) {
	t.Logger.Info(fmt.Sprintf(format, a...))
}

func (t *tclogr) Print(a ...interface{}) {
	t.Logger.Info(fmt.Sprint(a...))
}
