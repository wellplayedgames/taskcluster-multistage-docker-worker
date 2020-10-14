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
	Image   string   `json:"image"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`
}

// Payload contains the entire payload for a task.
type Payload struct {
	MaxRunTime int    `json:"maxRunTime,omitempty"`
	Steps      []Step `json:"steps"`
}
