package config

// ValueFromSecret is used to insert secret values.
type ValueFromSecret struct {
	SecretName string `json:"secretName"`
	Path       string `json:"path,omitempty"`
}

// ValueFrom is used to insert values from elsewhere.
type ValueFrom struct {
	ValueFromSecret *ValueFromSecret `json:"secret,omitempty"`
}

// EnvVar represents a single environment variable.
type EnvVar struct {
	Name      string     `json:"name"`
	Value     string     `json:"value,omitempty"`
	ValueFrom *ValueFrom `json:"valueFrom,omitempty"`
}

// Step is a single stage in the build process.
type Step struct {
	Image      string   `json:"image"`
	Command    []string `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Env        []EnvVar `json:"env,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
	Pull       *bool    `json:"pull,omitempty"`
	Privileged bool     `json:"privileged,omitempty"`
}

// ArtifactType is used to determine how to upload artifacts
type ArtifactType string

const (
	// FileArtifact uploads a single file verbatim
	FileArtifact ArtifactType = "file"
)

// Artifact is the information required to upload a single artifact.
type Artifact struct {
	Name        string       `json:"name,omitempty"`
	Path        string       `json:"path,omitempty"`
	Type        ArtifactType `json:"type,omitempty"`
	ContentType string       `json:"contentType,omitempty"`
}

// Payload contains the entire payload for a task.
type Payload struct {
	MaxRunTime int    `json:"maxRunTime,omitempty"`
	Steps      []Step `json:"steps,omitempty"`

	// Environment shared between steps.
	Env       []EnvVar   `json:"env,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}
