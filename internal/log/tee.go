package log

import "github.com/go-logr/logr"

type Tee struct {
	loggers []logr.Logger
}

var _ logr.Logger = (*Tee)(nil)

// NewTee creates a new tee logger.
func NewTee(loggers ...logr.Logger) *Tee {
	return &Tee{loggers}
}

// Enabled returns true if the log is enabled. (Tee is always enabled.)
func (t *Tee) Enabled() bool {
	return true
}

// Info prints a message to the log.
func (t *Tee) Info(msg string, keysAndValues ...interface{}) {
	for _, l := range t.loggers {
		l.Info(msg, keysAndValues...)
	}
}

// Error records an error in the log.
func (t *Tee) Error(err error, msg string, keysAndValues ...interface{}) {
	for _, l := range t.loggers {
		l.Error(err, msg, keysAndValues...)
	}
}

// V creates a new logger with the verbosity set to level.
func (t *Tee) V(level int) logr.Logger {
	logs := make([]logr.Logger, len(t.loggers))

	for idx, l := range t.loggers {
		logs[idx] = l.V(level)
	}

	return &Tee{logs}
}

// WithValues creates a new logger with the given values set.
func (t *Tee) WithValues(keysAndValues ...interface{}) logr.Logger {
	logs := make([]logr.Logger, len(t.loggers))

	for idx, l := range t.loggers {
		logs[idx] = l.WithValues(keysAndValues...)
	}

	return &Tee{logs}
}

// WithName creates a new logger with the given log name set.
func (t *Tee) WithName(name string) logr.Logger {
	logs := make([]logr.Logger, len(t.loggers))

	for idx, l := range t.loggers {
		logs[idx] = l.WithName(name)
	}

	return &Tee{logs}
}
