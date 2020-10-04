package cri

import (
	"context"
	"io"
	"time"

	"github.com/go-logr/logr"
)

// ContainerSpec contains a container specification.
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

// ContainerStatus contains information about the current state of a container.
type ContainerStatus struct {
	StartedAt  *time.Time
	FinishedAt *time.Time
	IPAddress  string
}

// CRI is an implementation of a container runtime.
type CRI interface {
	ImagePull(ctx context.Context, log logr.Logger, image string) error
	ContainerCreate(ctx context.Context, spec *ContainerSpec) (Container, error)
}

// Container represents a single container managed by a CRI.
type Container interface {
	ID() string
	Status(ctx context.Context) (*ContainerStatus, error)
	Run(ctx context.Context, stdout, stderr io.Writer) (int, error)
	Remove(ctx context.Context) error

	ReadFiles(ctx context.Context, path string) (io.ReadCloser, error)
}

// Sandbox represents a CRI which can be deleted.
type Sandbox interface {
	io.Closer
	CRI
}

// SandboxFactory is a factory which can create Sandboxes.
type SandboxFactory interface {
	SandboxCreate(ctx context.Context, log logr.Logger, name string) (Sandbox, error)
}
