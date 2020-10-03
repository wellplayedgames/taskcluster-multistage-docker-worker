package worker

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcauth"
	"github.com/taskcluster/taskcluster/v37/tools/websocktunnel/util"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/taskcluster/taskcluster/v37/tools/websocktunnel/client"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/pubsubbuffer"
)

type LiveLog struct {
	baseURL  string
	listener net.Listener
	server   *pubsubbuffer.Server
}

func NewLiveLog(log logr.Logger, config *Config) (*LiveLog, error) {
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

func (l *LiveLog) BaseURL() string {
	return l.baseURL
}

func (l *LiveLog) Allocate() (string, pubsubbuffer.WriteSubscribeCloser) {
	path, w := l.server.Allocate()
	url := l.baseURL + path
	return url, w
}

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

func createWSTListener(log logr.Logger, config *Config) (string, net.Listener, error) {
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

func createListener(log logr.Logger, config *Config) (url string, listener net.Listener, err error) {
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
