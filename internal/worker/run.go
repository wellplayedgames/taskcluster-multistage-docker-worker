package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/logrusorgru/aurora/v3"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcqueue"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcsecrets"
	"github.com/tidwall/gjson"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/config"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/cri"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/exception"
	lg "github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/log"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/pubsubbuffer"
	"github.com/wojas/genericr"
)

const (
	defaultTaskRunTimeout = 60 * 60 * time.Second
)

func (w *Worker) uploadLog(log logr.Logger, queue *tcqueue.Queue, claim *tcqueue.TaskClaim, contents pubsubbuffer.WriteSubscribeCloser) {
	err := createS3Artifact(queue, claim, liveLogBacking, "text/plain", time.Time(claim.Task.Expires), contents.Len(), contents.Subscribe(context.Background()))
	if err != nil {
		log.Error(err, "failed to upload live log")
		return
	}

	runIdStr := strconv.FormatInt(claim.RunID, 10)
	url, err := queue.GetArtifact_SignedURL(claim.Status.TaskID, runIdStr, liveLogBacking, time.Until(time.Time(claim.Task.Expires)))
	if err != nil {
		log.Error(err, "failed to get backing log URL")
		return
	}

	err = createRedirectArtifact(queue, claim, liveLogName, url.String(), "text/plain", time.Time(claim.Task.Expires))
	if err != nil {
		log.Error(err, "failed to redirect live log to backing")
		return
	}
}

func cleanupContainer(log logr.Logger, container cri.Container) {
	ctx := context.Background()
	err := container.Remove(ctx)
	if err != nil {
		log.Error(err, "failed to remove container", "container", container.ID())
	}
}

func watchContainer(ctx context.Context, log logr.Logger, container cri.Container, ch chan<- error) {
	var err error
	defer func() {
		ch <- err
	}()

	rd, wr := io.Pipe()
	defer lg.LogClose(log, wr, "Failed to close container log pipe")
	go lg.CopyToLogNoError(log, rd)
	exitCode, err := container.Run(ctx, wr, wr)
	log.Info("Container exited", "exitCode", exitCode)
}

func (w *Worker) resolveValueFrom(ctx context.Context, claim *tcqueue.TaskClaim, valueFrom *config.ValueFrom) (result string, err error) {
	if valueFrom == nil {
		return "", nil
	}

	if vfs := valueFrom.ValueFromSecret; vfs != nil {
		credentials := taskCredentials(&claim.Credentials)
		secrets := tcsecrets.New(credentials, w.config.RootURL)
		secrets.Context = ctx

		var secret *tcsecrets.Secret
		secret, err = secrets.Get(vfs.SecretName)
		if err != nil {
			return
		}

		value := gjson.Get(string(secret.Secret), vfs.Path)
		result = value.String()
	}

	return
}

func (w *Worker) runStep(ctx context.Context, log logr.Logger, sandbox cri.CRI, claim *tcqueue.TaskClaim, rootContainer cri.Container, stepIdx int, payload *config.Payload, deps []<-chan error, ch chan<- error) {
	pullLog := log.WithName(fmt.Sprintf("pull %d", stepIdx))
	log = log.WithName(fmt.Sprintf("step %d", stepIdx))
	step := &payload.Steps[stepIdx]

	var err error
	defer func() {
		ch <- err
		close(ch)
	}()

	// Pull the image.
	err = sandbox.ImagePull(ctx, pullLog, step.Image)
	if err != nil {
		return
	}

	// Wait for all dependencies.
	for _, ch := range deps {
		err = <-ch
		if err != nil {
			return
		}
	}

	env := map[string]string{
		"TASKCLUSTER_ROOT_URL": w.config.RootURL,
		"TASKCLUSTER_PROXY_URL": "http://localhost:8080/",
		"TASK_GROUP_ID": claim.Status.TaskGroupID,
		"TASK_ID": claim.Status.TaskID,
		"RUN_ID": strconv.FormatInt(claim.RunID, 10),
	}

	for idx := 0; idx < len(step.Env) + len(payload.Env); idx += 1 {
		var e *config.EnvVar

		if idx < len(payload.Env) {
			e = &payload.Env[idx]
		} else {
			e = &step.Env[idx - len(payload.Env)]
		}

		var value string
		value, err = w.resolveValueFrom(ctx, claim, e.ValueFrom)
		if err != nil {
			return
		}

		if e.Value != "" {
			value = e.Value
		}

		env[e.Name] = value
	}

	containerName := fmt.Sprintf("step-%d", stepIdx)
	container, err := sandbox.ContainerCreate(ctx, &cri.ContainerSpec{
		Name:       containerName,
		Image:      step.Image,
		Entrypoint: step.Command,
		Command:    step.Args,
		Env:        env,
		WorkingDir: "/workspace",
		Binds: []string{
			"/var/run/docker.sock:/var/run/docker.sock",
		},
		PodWith: rootContainer,
	})
	if err != nil {
		return
	}
	defer cleanupContainer(log, container)

	log.Info(aurora.Green("Starting step").String(), "command", step.Command, "args", step.Args)

	outRead, outWrite := io.Pipe()
	errRead, errWrite := io.Pipe()

	defer lg.LogClose(log, outWrite, "Failed to close stdout")
	defer lg.LogClose(log, errWrite, "Failed to close stderr")

	go lg.CopyToLogNoError(log, outRead)
	go lg.CopyToLogPrefixNoError(log, errRead, "\033[31m")

	var exitCode int
	exitCode, err = container.Run(ctx, outWrite, errWrite)
	if exitCode != 0 {
		err = fmt.Errorf("exited with code %d", exitCode)
	}

	log.Info(aurora.Green("Step completed").String())
}

func (w *Worker) runTaskLogic(ctx context.Context, syslog, log logr.Logger, slot int, claim *tcqueue.TaskClaim, wr io.Writer) error {
	var payload config.Payload
	err := json.Unmarshal(claim.Task.Payload, &payload)
	if err != nil {
		return exception.MalformedPayload(err)
	}

	maxRunTime := time.Duration(payload.MaxRunTime) * time.Second
	if maxRunTime <= 0 {
		maxRunTime = defaultTaskRunTimeout
	}

	var ctxCancel context.CancelFunc
	ctx, ctxCancel = context.WithTimeout(ctx, maxRunTime)
	defer ctxCancel()

	// Create Sandbox
	sandboxName := fmt.Sprintf("taskcluster_%s_%d", claim.Status.TaskID, claim.RunID)
	sandbox, err := w.sandboxFactory.SandboxCreate(ctx, syslog, sandboxName)
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to start sandbox: %w", err))
	}
	defer lg.LogClose(log, sandbox, "Error cleaning up sandbox")

	// Start Proxy
	err = sandbox.ImagePull(ctx, syslog, w.config.TaskclusterProxyImage)
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to pull taskcluster-oroxy: %w", err))
	}

	proxyContainer, err := sandbox.ContainerCreate(ctx, &cri.ContainerSpec{
		Name:  "taskcluster-proxy",
		Image: w.config.TaskclusterProxyImage,
		Volumes: map[string]struct{}{
			"/workspace": struct{}{},
			"/home":      struct{}{},
			"/root":      struct{}{},
		},
		Entrypoint: []string{"/taskcluster-proxy"},
		Env: map[string]string{
			"TASKCLUSTER_ROOT_URL":     w.config.RootURL,
			"TASKCLUSTER_CLIENT_ID":    claim.Credentials.ClientID,
			"TASKCLUSTER_ACCESS_TOKEN": claim.Credentials.AccessToken,
			"TASKCLUSTER_CERTIFICATE":  claim.Credentials.Certificate,
		},
	})
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to create taskcluster-proxy: %w", err))
	}
	defer cleanupContainer(log, proxyContainer)

	startCh := make(chan error, 1)
	nextCh := startCh
	exitCh := make(chan error, 1)
	go watchContainer(ctx, syslog, proxyContainer, exitCh)

	// Configure containers
	for idx := range payload.Steps {
		step := &payload.Steps[idx]
		stepCh := make(chan error, 1)

		go w.runStep(ctx, log, sandbox, claim, proxyContainer, idx, payload, []<-chan error{nextCh}, stepCh)
		nextCh = stepCh
	}

	// Start containers
	close(startCh)

	// Wait for something to happen.
	for {
		select {
		case err = <-nextCh:
			return err

		case err = <-exitCh:
			return err
		}
	}
}

// RunTask runs a single task run to completion.
func (w *Worker) RunTask(ctx context.Context, slot int, claim *tcqueue.TaskClaim) {
	log := w.log.WithValues("taskId", claim.Status.TaskID, "runId", claim.RunID)
	log.Info("Starting task run")
	defer log.Info("Finished task run")

	credentials := taskCredentials(&claim.Credentials)
	queue := tcqueue.New(credentials, w.config.RootURL)
	queue.Context = ctx
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	reclaimCh := time.After(time.Until(time.Time(claim.TakenUntil).Add(-reclaimSafetyInterval)))

	var exitErr error = exception.InternalError(fmt.Errorf("never finished"))
	logUrl, liveLog := w.livelog.Allocate()
	liveLogr := genericr.New(func(e genericr.Entry) {
		fmt.Fprintf(liveLog, "%s\n", lg.FancyLog(e))
	})
	defer func() {
		err := exitErr
		if err != nil {
			liveLogr.Error(err, "Task failed")
		} else {
			liveLogr.Info(aurora.Green("Task completed").String())
		}

		liveLog.Close()
		w.uploadLog(log, queue, claim, liveLog)

		if err == nil {
			_, err = queue.ReportCompleted(claim.Status.TaskID, runIdStr)
		} else {
			if x, ok := err.(exception.Error); ok {
				_, err = queue.ReportException(claim.Status.TaskID, runIdStr, &tcqueue.TaskExceptionRequest{
					Reason: x.ExceptionKind(),
				})
			} else {
				_, err = queue.ReportFailed(claim.Status.TaskID, runIdStr)
			}
		}

		if err != nil {
			log.Error(err, "error reporting task")
		}

		return
	}()

	liveLogr.Info(aurora.Green("Starting task on multistage-docker-worker").String())

	err := createRedirectArtifact(queue, claim, liveLogName, logUrl, "text/plain", time.Time(claim.Task.Expires))
	if err != nil {
		log.Error(err, "error creating live-log")
		queue.ReportException(claim.Status.TaskID, runIdStr, &tcqueue.TaskExceptionRequest{
			Reason: "internal-error",
		})
		return
	}

	var cancelCtx context.CancelFunc
	ctx, cancelCtx = context.WithCancel(ctx)
	defer cancelCtx()

	doneCh := make(chan error)

	go func() {
		var err error = exception.InternalError(fmt.Errorf("task panic"))
		defer func() {
			doneCh <- err
		}()
		err = w.runTaskLogic(ctx, log, liveLogr, slot, claim, liveLog)
	}()

	for {
		select {
		case <-reclaimCh:
			resp, err := queue.ReclaimTask(claim.Status.TaskID, runIdStr)
			if err != nil {
				log.Error(err, "failed to reclaim task")
				return
			}
			claim.Status = resp.Status
			claim.Credentials = resp.Credentials
			claim.TakenUntil = resp.TakenUntil
			credentials = taskCredentials(&resp.Credentials)

		case exitErr = <-doneCh:
			return
		}
	}
}
