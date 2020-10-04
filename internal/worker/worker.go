package worker

import (
	"context"
	"os"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/go-logr/logr"
	tcclient "github.com/taskcluster/taskcluster/v37/clients/client-go"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcqueue"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/config"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/cri"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/livelog"
)

const (
	liveLogName    = "public/logs/live.log"
	liveLogBacking = "public/logs/live-backing.log"

	reclaimSafetyInterval = 30 * time.Second
)

func taskCredentials(c *tcqueue.TaskCredentials) *tcclient.Credentials {
	return &tcclient.Credentials{
		ClientID:    c.ClientID,
		AccessToken: c.AccessToken,
		Certificate: c.Certificate,
	}
}

// Worker is the implementation of the worker lifecycle.
type Worker struct {
	config         *config.Config
	livelog        *livelog.LiveLog
	log            logr.Logger
	sandboxFactory cri.SandboxFactory
}

// NewWorker creates a new worker given the configuration and sandbox factory.
func NewWorker(log logr.Logger, config *config.Config, factory cri.SandboxFactory) (*Worker, error) {
	// Make tasks directory.
	if info, err := os.Stat(config.TasksDir); (err != nil) || !info.IsDir() {
		err = os.Mkdir(config.TasksDir, 0775)
		if err != nil {
			return nil, err
		}
	}

	l, err := livelog.New(log, config)
	if err != nil {
		return nil, err
	}

	w := &Worker{
		config:         config,
		livelog:        l,
		log:            log,
		sandboxFactory: factory,
	}
	return w, nil
}

// Run the worker.
func (w *Worker) Run(ctx context.Context, gracefulStop <-chan struct{}) error {
	config := w.config

	go func() {
		err := w.livelog.Serve(ctx)
		w.log.Error(err, "LiveLog closed")
	}()

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	safeCtx, safeCancel := context.WithCancel(ctx)
	defer safeCancel()
	go func() {
		<-gracefulStop
		safeCancel()
	}()

	credentials := config.Credentials()
	queue := tcqueue.New(credentials, config.RootURL)
	queue.Context = ctx

	completedTasks := make(chan int, config.ConcurrentTasks)
	freeSlots := make([]int, config.ConcurrentTasks)
	for i := range freeSlots {
		freeSlots[i] = i
	}

	workReq := make(chan int)
	workRes := make(chan *tcqueue.ClaimWorkResponse)
	defer close(workReq)

	// A background goroutine for requesting work.
	go func() {
		safeQueue := queue
		queue.Context = safeCtx
		exponentialBackoff := backoff.NewExponentialBackOff()
		exponentialBackoff.MaxElapsedTime = 0
		bo := backoff.WithContext(exponentialBackoff, safeCtx)

		for req := range workReq {
			backoff.Retry(func() error {
				for {
					resp, err := safeQueue.ClaimWork(config.ProvisionerID, config.WorkerType, &tcqueue.ClaimWorkRequest{
						Tasks:       int64(req),
						WorkerGroup: config.WorkerType,
						WorkerID:    config.WorkerID,
					})
					if err != nil {
						w.log.Error(err, "error claiming work")
						return err
					} else if len(resp.Tasks) > 0 {
						workRes <- resp
						return nil
					}
				}
			}, bo)
		}
	}()

	workReq <- config.ConcurrentTasks
	isFull := false

	for {
		select {
		// Been told to exit.
		case <-ctx.Done():
			if len(freeSlots) == config.ConcurrentTasks {
				return ctx.Err()
			}

		// Run finished.
		case idx := <-completedTasks:
			freeSlots = append(freeSlots, idx)
			if (ctx.Err() != nil) && (len(freeSlots) == config.ConcurrentTasks) {
				return ctx.Err()
			}

			if isFull {
				isFull = false
				workReq <- 1
			}

		// New work claimed.
		case resp := <-workRes:
			for idx := range resp.Tasks {
				slot := freeSlots[len(freeSlots)-1]
				freeSlots = freeSlots[:len(freeSlots)-1]
				claim := &resp.Tasks[idx]

				go func() {
					defer func() {
						completedTasks <- slot
					}()
					w.RunTask(ctx, slot, claim)
				}()
			}

			if len(freeSlots) > 0 {
				workReq <- len(freeSlots)
			} else {
				isFull = true
			}
		}
	}
}
