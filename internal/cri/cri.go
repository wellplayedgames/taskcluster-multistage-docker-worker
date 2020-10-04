package cri

import (
	"context"
	"io"
	"time"

	"github.com/go-logr/logr"
)

type ContainerSpec struct {
	Name string

	Image      string
	Entrypoint []string
	Command    []string
	Env        map[string]string
	WorkingDir string
	Volumes    map[string]struct{}
	Binds      []string

	Privileged bool
	PodWith    Container
}

type ContainerStatus struct {
	StartedAt  *time.Time
	FinishedAt *time.Time
	IPAddress  string
}

type CRI interface {
	ImagePull(ctx context.Context, log logr.Logger, image string) error
	ContainerCreate(ctx context.Context, spec *ContainerSpec) (Container, error)
}

type Container interface {
	ID() string
	Status(ctx context.Context) (*ContainerStatus, error)
	Run(ctx context.Context, stdout, stderr io.Writer) (int, error)
	Remove(ctx context.Context) error

	ReadFiles(ctx context.Context, path string) (io.ReadCloser, error)
}

type Sandbox interface {
	io.Closer
	CRI
}

type SandboxFactory interface {
	SandboxCreate(ctx context.Context, log logr.Logger, name string) (Sandbox, error)
}
