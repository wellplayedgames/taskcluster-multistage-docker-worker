package cri

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/docker/docker/client"
	"github.com/go-logr/logr"
	lg "github.com/wellplayedgames/taskcluster-multistage-docker-worker/internal/log"
)

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

type dindFactory struct {
	CRI
	image string
}

var _ SandboxFactory = (*dindFactory)(nil)

func NewDockerInDockerSandbox(cri CRI, image string) SandboxFactory {
	d := &dindFactory{
		CRI:   cri,
		image: image,
	}
	return d
}

func (d *dindFactory) SandboxCreate(ctx context.Context, log logr.Logger, name string) (Sandbox, error) {
	err := d.ImagePull(ctx, log, d.image)
	if err != nil {
		return nil, fmt.Errorf("error pulling dind image: %w", err)
	}

	var cancelCtx context.CancelFunc
	ctx, cancelCtx = context.WithCancel(ctx)
	defer cancelCtx()

	spec := ContainerSpec{
		Name: name,
		Image: d.image,
		Env: map[string]string{
			"DOCKER_TLS_CERTDIR": "/certs",
		},
		Volumes: map[string]struct{}{
			"/var/lib/docker": struct{}{},
		},
		Privileged: true,
	}

	container, err := d.ContainerCreate(ctx, &spec)
	if err != nil {
		return nil, err
	}

	wasClean := false
	defer func() {
		if !wasClean {
			err := container.Remove(context.Background())
			if err != nil {
				log.Error(err, "Failed to remove docker-in-docker")
			}
		}
	}()

	stdoutRead, stdoutWrite := io.Pipe()

	go func() {
		defer stdoutWrite.Close()
		exitCode, err := container.Run(context.Background(), stdoutWrite, stdoutWrite)
		if err == nil && exitCode != 0 {
			err = fmt.Errorf("exited with code %d", exitCode)
		}

		if err != nil {
			log.Error(err, "DinD exited with an error")
		}
	}()
	go lg.CopyToLogNoError(log, stdoutRead)

	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = time.Second * 30

	// Wait for container to start
	var status *ContainerStatus
	err = backoff.Retry(func() error {
		var err error
		status, err = container.Status(ctx)
		if err != nil {
			return err
		}

		if status.StartedAt == nil {
			return fmt.Errorf("container not started yet")
		}

		if status.FinishedAt != nil {
			return backoff.Permanent(fmt.Errorf("container exited"))
		}

		if status.IPAddress == "" {
			return fmt.Errorf("container not no network")
		}

		return nil
	}, bo)
	if err != nil {
		return nil, err
	}

	// Connect to DinD
	transport := http.Transport{TLSClientConfig: &tls.Config{}}
	httpClient := http.Client{Transport: &transport}

	dndHost := fmt.Sprintf("tcp://%s:2376", status.IPAddress)
	dindClient, err := client.NewClient(dndHost, client.DefaultVersion, &httpClient, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to dind: %w", err)
	}

	// Wait for DinD to be ready.
	err = backoff.Retry(func() error {
		_, err := dindClient.Info(ctx)
		if client.IsErrConnectionFailed(err) {
			return err
		}

		return nil
	}, bo)
	if err != nil {
		return nil, err
	}

	rd, err := container.ReadFiles(ctx, "/certs/client")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch dind tls: %w", err)
	}

	tlsConfig, err := tlsConfigFromTar(log, rd)
	rd.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to configure dind tls: %w", err)
	}

	transport.TLSClientConfig = tlsConfig

	info, err := dindClient.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch docker info: %w", err)
	}
	log.Info("Connected to docker",
		"version", info.ServerVersion)

	wasClean = true
	sandbox := &dindSandbox{
		CRI:       &Docker{dindClient},
		container: container,
	}
	return sandbox, nil
}

type dindSandbox struct {
	CRI
	container Container
}

var _ Sandbox = (*dindSandbox)(nil)

func (d *dindSandbox) Close() error {
	return d.container.Remove(context.Background())
}
