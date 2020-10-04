package worker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/go-logr/logr"
	"github.com/logrusorgru/aurora/v3"
	tcclient "github.com/taskcluster/taskcluster/v37/clients/client-go"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcqueue"
	"github.com/taskcluster/taskcluster/v37/clients/client-go/tcsecrets"
	"github.com/tidwall/gjson"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/exception"
	"github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/pubsubbuffer"
	"github.com/wojas/genericr"
)

const (
	liveLogName    = "public/logs/live.log"
	liveLogBacking = "public/logs/live-backing.log"

	dockerServerPort = nat.Port("2376/tcp")

	reclaimSafetyInterval = 30 * time.Second
)

func taskCredentials(c *tcqueue.TaskCredentials) *tcclient.Credentials {
	return &tcclient.Credentials{
		ClientID:    c.ClientID,
		AccessToken: c.AccessToken,
		Certificate: c.Certificate,
	}
}

func copyToLog(r io.Reader, log logr.Logger) error {
	rd := bufio.NewReader(r)

	for {
		line, _, err := rd.ReadLine()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		log.Info(string(line))
	}
}

func prettyValue(v interface{}) string {
	s := fmt.Sprintf("%v", v)
	by, _ := json.Marshal(&s)
	return string(by)
}

func fancyLog(e genericr.Entry)  string {
	now := time.Now().UTC().Format(time.RFC3339)[:20]

	// Find step key
	stepIdx := -1
	step := ""
	for i := 0; i < len(e.Fields); i += 2 {
		if s, ok := e.Fields[i].(string); ok && s == "step" {
			stepIdx = i
			step = fmt.Sprintf("%v", e.Fields[i + 1])
			break
		}
	}

	buf := bytes.NewBuffer(make([]byte, 0, 160))
	buf.WriteString(now)

	if len(e.Name) > 0 {
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
	}

	if len(step) > 0 {
		buf.WriteString(" step ")
		buf.WriteString(step)
	}

	l := buf.Len()
	for l < 30 {
		buf.WriteByte(' ')
		l += 1
	}

	if e.Error != nil {
		buf.WriteString(aurora.Red(e.Message).String())
	} else {
		buf.WriteString(e.Message)
	}

	if e.Error != nil {
		buf.WriteString(" error=")
		buf.WriteString(prettyValue(e.Error.Error()))
	}

	for i := 0; i < len(e.Fields); i += 2 {
		if i == stepIdx {
			continue
		}

		buf.WriteByte(' ')
		if s, ok := e.Fields[i].(string); ok {
			if s == "" {
				continue
			}

			buf.WriteString(s)
		} else {
			buf.WriteString(prettyValue(e.Fields[i]))
		}
		buf.WriteByte('=')

		if len(e.Fields) > i {
			buf.WriteString(prettyValue(e.Fields[i+1]))
		} else {
			buf.WriteString("error")
		}
	}

	return buf.String()
}

func parseDockerLogs(r io.Reader, w io.Writer) error {
	max := 4096
	buf := make([]byte, max)

	for {
		_, err := io.ReadFull(r, buf[:8])
		if err != nil {
			return err
		}

		streamType := buf[0]
		header := "\033[0m"
		if streamType == 2 {
			header = fmt.Sprintf("\033[%sm", aurora.RedFg.Nos(true))
		}

		_, err = w.Write([]byte(header))
		if err != nil {
			return err
		}

		n := int(binary.BigEndian.Uint32(buf[4:8]))
		for n > 0 {
			toRead := int(n)
			if toRead > max {
				toRead = max
			}

			_, err := io.ReadFull(r, buf[:toRead])
			if err != nil {
				return err
			}


			_, err = w.Write(buf[:toRead])
			if err != nil {
				return err
			}

			n -= toRead
		}
	}
}

func dockerLogs(ctx context.Context, log logr.Logger, cl client.APIClient, id string) error {
	rd, err := cl.ContainerLogs(ctx, id, types.ContainerLogsOptions{
		Follow:     true,
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return err
	}
	defer rd.Close()

	pr, pw := io.Pipe()

	go func() {
		err := fmt.Errorf("panic")
		defer func() {
			pw.CloseWithError(err)
		}()
		err = parseDockerLogs(rd, pw)
	}()

	return copyToLog(pr, log)
}

func canonicalImage(image string) string {
	repo := image
	tagOffset := strings.IndexRune(image, ':')
	if tagOffset >= 0 {
		repo = image[:tagOffset]
	}

	numSlashes := strings.Count(repo, "/")
	switch numSlashes {
	case 0:
		return "docker.io/library/" + image

	case 1:
		return "docker.io/" + image

	default:
		return image
	}
}

func dockerPull(ctx context.Context, log logr.Logger, cl client.APIClient, image string) (err error) {
	r, err := cl.ImagePull(ctx, canonicalImage(image), types.ImagePullOptions{})
	if err != nil {
		return
	}
	defer func() {
		cerr := r.Close()
		if err == nil {
			err = cerr
		}
	}()

	br := bufio.NewReader(r)

	for {
		var status struct {
			Status string `json:"status"`
			ID     string `json:"id"`
		}

		line, _, lerr := br.ReadLine()
		if lerr == io.EOF {
			return
		} else if lerr != nil {
			err = lerr
			return
		}

		lerr = json.Unmarshal(line, &status)
		if lerr == nil {
			if !strings.HasSuffix(status.Status, "ing") {
				log.Info(status.Status, "id", status.ID)
			}
		} else {
			log.Info(string(line))
		}
	}
}

func createRedirectArtifact(queue *tcqueue.Queue, claim *tcqueue.TaskClaim, name, url, contentType string, expires time.Time) error {
	createReq := tcqueue.RedirectArtifactRequest{
		ContentType: contentType,
		Expires:     tcclient.Time(expires),
		StorageType: "reference",
		URL:         url,
	}

	reqBy, err := json.Marshal(&createReq)
	if err != nil {
		return err
	}

	req := tcqueue.PostArtifactRequest(reqBy)
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	_, err = queue.CreateArtifact(claim.Status.TaskID, runIdStr, name, &req)
	return err
}

func createS3Artifact(queue *tcqueue.Queue, claim *tcqueue.TaskClaim, name, contentType string, expires time.Time, contentLen int, r io.Reader) error {
	createReq := tcqueue.S3ArtifactRequest{
		ContentType: contentType,
		Expires:     tcclient.Time(expires),
		StorageType: "s3",
	}

	reqBy, err := json.Marshal(&createReq)
	if err != nil {
		return err
	}

	req := tcqueue.PostArtifactRequest(reqBy)
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	respBy, err := queue.CreateArtifact(claim.Status.TaskID, runIdStr, name, &req)
	if err != nil {
		return err
	}

	var resp tcqueue.S3ArtifactResponse
	if err := json.Unmarshal(*respBy, &resp); err != nil {
		return err
	}

	putReq, err := http.NewRequest(http.MethodPut, resp.PutURL, r)
	if err != nil {
		return err
	}

	putReq.Header.Set("Content-Type", resp.ContentType)
	putReq.ContentLength = int64(contentLen)

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode >= 300 {
		bodyText, _ := ioutil.ReadAll(putResp.Body)

		return fmt.Errorf("unexpected status code creating artifact: %d: %s", putResp.StatusCode, string(bodyText))
	}

	return nil
}

type Worker struct {
	config  *Config
	livelog *LiveLog
	log     logr.Logger
	docker  client.APIClient
}

func NewWorker(log logr.Logger, config *Config, docker client.APIClient) (*Worker, error) {
	// Make tasks directory.
	if info, err := os.Stat(config.TasksDir); (err != nil) || !info.IsDir() {
		err = os.Mkdir(config.TasksDir, 0775)
		if err != nil {
			return nil, err
		}
	}

	l, err := NewLiveLog(log, config)
	if err != nil {
		return nil, err
	}

	w := &Worker{
		config:  config,
		livelog: l,
		log:     log,
		docker:  docker,
	}
	return w, nil
}

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

	if w.config.DockerExternalIP != nil {
		hostConfig.PortBindings = map[nat.Port][]nat.PortBinding{
			dockerServerPort: {
				{
					HostIP: *w.config.DockerExternalIP,
				},
			},
		}
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
	if w.config.DockerExternalIP != nil {
		bindings := dindInspect.NetworkSettings.Ports[dockerServerPort]
		hostPort := bindings[0].HostPort
		dndHost = fmt.Sprintf("tcp://%s:%s", *w.config.DockerExternalIP, hostPort)
	}

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
	log = log.WithValues("step", stepIdx)

	var err error
	defer func() {
		ch <- err
		close(ch)
	}()

	// Pull the image.
	err = dockerPull(ctx, log, cl, step.Image)
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
	log.Info(aurora.Green("Starting task run").String())
	defer log.Info(aurora.Green("Finished task run").String())

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
