package cri

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/docker/distribution/reference"
	"io"
	"io/ioutil"
	"strings"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	ctypes "github.com/docker/cli/cli/config/types"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/go-logr/logr"
)

var (
	dockerEpoch = time.Time{}
)

// Docker is a Docker CRI implementation.
type Docker struct {
	client client.APIClient
	config *configfile.ConfigFile
}

var _ CRI = (*Docker)(nil)

// NewDocker wraps a Docker client and config file as a CRI.
func NewDocker(client client.APIClient, config *configfile.ConfigFile) *Docker {
	return &Docker{
		client: client,
		config: config,
	}
}

func encodeAuthToBase64(authConfig ctypes.AuthConfig) (string, error) {
	by, err := json.Marshal(authConfig)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(by), nil
}

// ImagePull fetches a remote image into the local Docker instance.
func (d *Docker) ImagePull(ctx context.Context, log logr.Logger, image string) (err error) {
	canonImage, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return fmt.Errorf("error pulling %s: %w", image, err)
	}

	domain := reference.Domain(canonImage)
	authConfig, err := d.config.GetAuthConfig(domain)
	if err != nil {
		return fmt.Errorf("error pulling %s: %w", image, err)
	}

	encodedAuth, err := encodeAuthToBase64(authConfig)
	if err != nil {
		return fmt.Errorf("error encoding auth: %w", err)
	}

	r, err := d.client.ImagePull(ctx, canonImage.String(), types.ImagePullOptions{
		RegistryAuth:  encodedAuth,
	})
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
				if status.ID == "" {
					log.Info(status.Status)
				} else {
					log.Info(fmt.Sprintf("%s: %s", status.ID, status.Status))
				}
			}
		} else {
			log.Info(string(line))
		}
	}
}

// ContainerCreate creates a new Docker container.
func (d *Docker) ContainerCreate(ctx context.Context, spec *ContainerSpec) (Container, error) {
	envList := make([]string, 0, len(spec.Env))

	for k, v := range spec.Env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}

	config := &container.Config{
		Image: spec.Image,

		Entrypoint: spec.Entrypoint,
		Cmd:        spec.Command,
		Env:        envList,
		WorkingDir: spec.WorkingDir,
		Volumes:    spec.Volumes,
	}

	hostConfig := &container.HostConfig{
		Binds:      spec.Binds,
		Privileged: spec.Privileged,
	}

	if spec.PodWith != nil {
		podID := spec.PodWith.ID()
		hostConfig.NetworkMode = container.NetworkMode(fmt.Sprintf("container:%s", podID))
		hostConfig.VolumesFrom = []string{podID}
	}

	container, err := d.client.ContainerCreate(ctx, config, hostConfig, nil, spec.Name)
	if err != nil {
		return nil, err
	}

	c := &dockerContainer{
		client: d.client,
		id: container.ID,
	}
	return c, nil
}

type dockerContainer struct {
	client client.APIClient
	id     string
}

var _ Container = (*dockerContainer)(nil)

func (d *dockerContainer) ID() string {
	return d.id
}

func (d *dockerContainer) Status(ctx context.Context) (*ContainerStatus, error) {
	resp, err := d.client.ContainerInspect(ctx, d.id)
	if err != nil {
		return nil, err
	}

	startedAt, err := time.Parse(time.RFC3339Nano, resp.State.StartedAt)
	if err != nil {
		return nil, err
	}

	finishedAt, err := time.Parse(time.RFC3339Nano, resp.State.FinishedAt)
	if err != nil {
		return nil, err
	}

	status := ContainerStatus{
		IPAddress: resp.NetworkSettings.IPAddress,
	}

	if startedAt != dockerEpoch {
		status.StartedAt = &startedAt
	}

	if finishedAt != dockerEpoch {
		status.FinishedAt = &finishedAt
	}

	return &status, nil
}

func (d *dockerContainer) Run(ctx context.Context, stdout, stderr io.Writer) (int, error) {
	if stdout == nil {
		stdout = ioutil.Discard
	}

	if stderr == nil {
		stderr = ioutil.Discard
	}

	err := d.client.ContainerStart(ctx, d.id, types.ContainerStartOptions{})
	if err != nil {
		return 0, err
	}

	rd, err := d.client.ContainerLogs(ctx, d.id, types.ContainerLogsOptions{
		Follow:     true,
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return 0, err
	}
	defer rd.Close()

	max := 4096
	buf := make([]byte, max)

	for {
		_, err := io.ReadFull(rd, buf[:8])
		if err == io.EOF {
			resp, err := d.client.ContainerInspect(ctx, d.id)
			if err != nil {
				return 0, err
			}

			return resp.State.ExitCode, nil
		} else if err != nil {
			return 0, err
		}

		streamType := buf[0]
		var w io.Writer
		switch streamType {
		case 0:
			panic("got stdin on docker logs")
		case 1:
			w = stdout
		case 2:
			w = stderr
		}

		n := int(binary.BigEndian.Uint32(buf[4:8]))
		for n > 0 {
			toRead := n
			if toRead > max {
				toRead = max
			}

			_, err := io.ReadFull(rd, buf[:toRead])
			if err != nil {
				return 0, err
			}

			_, err = w.Write(buf[:toRead])
			if err != nil {
				return 0, err
			}

			n -= toRead
		}
	}
}

func (d *dockerContainer) Remove(ctx context.Context) error {
	return d.client.ContainerRemove(ctx, d.id, types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	})
}

func (d *dockerContainer) ReadFiles(ctx context.Context, path string) (io.ReadCloser, error) {
	rd, _, err := d.client.CopyFromContainer(ctx, d.id, path)
	return rd, err
}
