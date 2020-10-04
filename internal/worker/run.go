package worker

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/go-logr/logr"
	"github.com/logrusorgru/aurora/v3"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcqueue"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcsecrets"
	"github.com/tidwall/gjson"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/exception"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/pubsubbuffer"
	"github.com/wojas/genericr"
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

func tlsConfigFromTar(log logr.Logger, r io.Reader) (*tls.Config, error) {
	certPool, err := x509.SystemCertPool()
	if err != nil {
		certPool = x509.NewCertPool()
	}

	config := tls.Config{
		RootCAs: certPool,
	}

	var cert []byte
	var key []byte

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return &config, nil
		} else if err != nil {
			return nil, err
		}

		if hdr.FileInfo().IsDir() {
			continue
		}

		fileContent, err := ioutil.ReadAll(tr)
		if err != nil {
			return nil, err
		}

		switch hdr.Name {
		case "client/cert.pem":
			cert = fileContent

		case "client/key.pem":
			key = fileContent

		case "client/ca.pem":
			certPool.AppendCertsFromPEM(fileContent)

		case "client/openssl.cnf":
		case "client/csr.pem":

		default:
			log.Info("unexpected file", "file", hdr.Name)
		}

		if cert != nil && key != nil {
			parsedCert, err := tls.X509KeyPair(cert, key)
			if err != nil {
				return nil, err
			}
			config.Certificates = append(config.Certificates, parsedCert)
			cert = nil
			key = nil
		}
	}
}

func cleanupContainer(log logr.Logger, cl client.APIClient, id string) {
	ctx := context.Background()
	err := cl.ContainerRemove(ctx, id, types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	})
	if err != nil {
		log.Error(err, "failed to remove proxy container")
	}
}

func (w *Worker) startDind(ctx context.Context, log logr.Logger, claim *tcqueue.TaskClaim, dir, workspaceDir string) (client.APIClient, string, error) {
	var cancelCtx context.CancelFunc
	ctx, cancelCtx = context.WithCancel(ctx)
	defer cancelCtx()

	dockerName := fmt.Sprintf("taskcluster_%s_%d", claim.Status.TaskID, claim.RunID)
	certPath := filepath.Join(dir, "certs")
	if err := os.Mkdir(certPath, 0775); err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("error creating certs dir: %w", err))
	}

	config := &container.Config{
		Image: w.config.DindImage,
		Env: []string{
			"DOCKER_TLS_CERTDIR=/certs",
		},
		Volumes: map[string]struct{}{
			"/var/lib/docker": struct{}{},
			"/certs":          struct{}{},
		},
	}

	hostConfig := &container.HostConfig{
		Privileged: true,
	}

	dindContainer, err := w.docker.ContainerCreate(ctx, config, hostConfig, nil, dockerName)
	wasClean := false
	defer func() {
		if !wasClean {
			cleanupContainer(log, w.docker, dindContainer.ID)
		}
	}()

	err = w.docker.ContainerStart(ctx, dindContainer.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("failed to start dind: %w", err))
	}

	dindInspect, err := w.docker.ContainerInspect(ctx, dindContainer.ID)
	if err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("failed to fetch dind info: %w", err))
	}

	// Connect to DinD
	transport := http.Transport{TLSClientConfig: &tls.Config{}}
	httpClient := http.Client{Transport: &transport}

	dndHost := fmt.Sprintf("tcp://%s:2376", dindInspect.NetworkSettings.IPAddress)
	dindClient, err := client.NewClient(dndHost, client.DefaultVersion, &httpClient, nil)
	if err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("failed to connect to dind: %w", err))
	}

	// Wait for DinD to be ready.
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = time.Second * 30
	err = backoff.Retry(func() error {
		_, err := dindClient.Info(ctx)
		if client.IsErrConnectionFailed(err) {
			return exception.InternalError(err)
		}

		return nil
	}, bo)
	if err != nil {
		return nil, "", err
	}

	rd, _, err := w.docker.CopyFromContainer(ctx, dindContainer.ID, "/certs/client")
	if err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("failed to fetch dind tls: %w", err))
	}

	tlsConfig, err := tlsConfigFromTar(log, rd)
	rd.Close()
	if err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("failed to configure dind tls: %w", err))
	}

	transport.TLSClientConfig = tlsConfig

	info, err := dindClient.Info(ctx)
	if err != nil {
		return nil, "", exception.InternalError(fmt.Errorf("failed to fetch docker info: %w", err))
	}
	log.Info("Connected to docker",
		"version", info.ServerVersion)

	wasClean = true
	return dindClient, dindContainer.ID, nil
}

func watchContainer(ctx context.Context, log logr.Logger, cl client.APIClient, id string, ch chan<- error) {
	var err error
	defer func() {
		ch <- err
	}()

	err = dockerLogs(ctx, log, cl, id)
	if err != nil {
		return
	}

	resp, err := cl.ContainerInspect(ctx, id)
	if err != nil {
		return
	}

	if resp.State.Running {
		panic(fmt.Errorf("container was still running"))
	}

	log.Info("exited with code: %d\n", resp.State.ExitCode)
}

func (w *Worker) resolveValueFrom(ctx context.Context, claim *tcqueue.TaskClaim, valueFrom *ValueFrom) (result string, err error) {
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

func (w *Worker) runStep(ctx context.Context, log logr.Logger, wr io.Writer, cl client.APIClient, claim *tcqueue.TaskClaim, rootContainer string, stepIdx int, step *Step, deps []<-chan error, ch chan<- error) {
	pullLog := log.WithName(fmt.Sprintf("pull %d", stepIdx))
	log = log.WithName(fmt.Sprintf("step %d", stepIdx))

	var err error
	defer func() {
		ch <- err
		close(ch)
	}()

	// Pull the image.
	err = dockerPull(ctx, pullLog, cl, step.Image)
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

	env := []string{
		"TASKCLUSTER_PROXY_URL=http://localhost:8080/",
	}

	for idx := range step.Env {
		var value string
		e := &step.Env[idx]
		value, err = w.resolveValueFrom(ctx, claim, e.ValueFrom)
		if err != nil {
			return
		}

		if e.Value != "" {
			value = e.Value
		}

		env = append(env, fmt.Sprintf("%s=%s", e.Name, value))
	}

	containerName := fmt.Sprintf("step-%d", stepIdx)
	container, err := cl.ContainerCreate(ctx, &container.Config{
		Image:      step.Image,
		Entrypoint: step.Command,
		Cmd:        step.Args,
		Env:        env,
		WorkingDir: "/workspace",
	}, &container.HostConfig{
		Binds: []string{
			"/var/run/docker.sock:/var/run/docker.sock",
		},
		NetworkMode: container.NetworkMode(fmt.Sprintf("container:%s", rootContainer)),
		VolumesFrom: []string{rootContainer},
	}, nil, containerName)
	if err != nil {
		return
	}
	defer cleanupContainer(log, cl, container.ID)

	log.Info(aurora.Green("Starting step").String(), "command", step.Command, "args", step.Args)

	err = cl.ContainerStart(ctx, container.ID, types.ContainerStartOptions{})
	if err != nil {
		return
	}

	err = dockerLogs(ctx, log, cl, container.ID)
	if err != nil {
		return
	}

	resp, err := cl.ContainerInspect(ctx, container.ID)
	if err != nil {
		return
	}

	if resp.State.ExitCode != 0 {
		err = fmt.Errorf("exited with code %d", resp.State.ExitCode)
	}

	log.Info(aurora.Green("Step completed").String())
}

func (w *Worker) runTaskLogic(ctx context.Context, syslog, log logr.Logger, slot int, claim *tcqueue.TaskClaim, wr io.Writer) error {
	var payload Payload
	err := json.Unmarshal(claim.Task.Payload, &payload)
	if err != nil {
		return exception.MalformedPayload(err)
	}

	// Create working directory.
	dir := filepath.Join(w.config.TasksDir, fmt.Sprintf("%s_%d", claim.Status.TaskID, claim.RunID))
	err = os.Mkdir(dir, 0775)
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to create task dir: %w", err))
	}
	defer os.RemoveAll(dir)

	workspaceDir := filepath.Join(dir, "workspace")
	err = os.Mkdir(workspaceDir, 0775)
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to create workspace: %w", err))
	}

	// Pull DinD
	err = dockerPull(ctx, syslog, w.docker, w.config.DindImage)
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to pull dind: %w", err))
	}

	// Start DinD
	dindClient, dindID, err := w.startDind(ctx, log, claim, dir, workspaceDir)
	if err != nil {
		return err
	}
	defer cleanupContainer(log, w.docker, dindID)

	// Start Proxy
	err = dockerPull(ctx, syslog, dindClient, w.config.TaskclusterProxyImage)
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to pull taskcluster-oroxy: %w", err))
	}

	proxyContainer, err := dindClient.ContainerCreate(ctx, &container.Config{
		Image: w.config.TaskclusterProxyImage,
		Volumes: map[string]struct{}{
			"/workspace": struct{}{},
			"/home": struct{}{},
			"/root": struct{}{},
		},
		Entrypoint: []string{"/taskcluster-proxy"},
		Env: []string{
			fmt.Sprintf("TASKCLUSTER_ROOT_URL=%s", w.config.RootURL),
			fmt.Sprintf("TASKCLUSTER_CLIENT_ID=%s", claim.Credentials.ClientID),
			fmt.Sprintf("TASKCLUSTER_ACCESS_TOKEN=%s", claim.Credentials.AccessToken),
			fmt.Sprintf("TASKCLUSTER_CERTIFICATE=%s", claim.Credentials.Certificate),
		},
	}, nil, nil, "taskcluster-proxy")
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to create taskcluster-proxy: %w", err))
	}
	defer cleanupContainer(log, dindClient, proxyContainer.ID)

	err = dindClient.ContainerStart(ctx, proxyContainer.ID, types.ContainerStartOptions{})
	if err != nil {
		return exception.InternalError(fmt.Errorf("failed to start proxy: %w", err))
	}

	startCh := make(chan error, 1)
	nextCh := startCh
	exitCh := make(chan error, 2)
	go watchContainer(ctx, syslog, w.docker, dindID, exitCh)
	go watchContainer(ctx, syslog, dindClient, proxyContainer.ID, exitCh)

	// Configure containers
	for idx := range payload.Steps {
		step := &payload.Steps[idx]
		stepCh := make(chan error, 1)

		go w.runStep(ctx, log, wr, dindClient, claim, proxyContainer.ID, idx, step, []<-chan error{nextCh}, stepCh)
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

	credentials := w.config.Credentials()
	queue := tcqueue.New(credentials, w.config.RootURL)
	queue.Context = ctx
	credentials = taskCredentials(&claim.Credentials)
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	reclaimCh := time.After(time.Until(time.Time(claim.TakenUntil).Add(-reclaimSafetyInterval)))

	var exitErr error = exception.InternalError(fmt.Errorf("never finished"))
	logUrl, liveLog := w.livelog.Allocate()
	liveLogr := genericr.New(func(e genericr.Entry) {
		fmt.Fprintf(liveLog, "%s\n", fancyLog(e))
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
