package workerproto

import (
	"fmt"
	"github.com/go-logr/logr"
)

type logPacket struct {
	Type string                 `json:"type"`
	Body map[string]interface{} `json:"body"`
}

type logger struct {
	communicator *Communicator
	base         logr.Logger
	name         string
	values       []interface{}
}

var _ logr.Logger = (*logger)(nil)

func (l *logger) sendLog(payload map[string]interface{}) {
	if !l.communicator.HasCapability("log") {
		return
	}

	packet := logPacket{
		Type: "log",
		Body: payload,
	}
	err := l.communicator.Send(&packet)
	if err != nil {
		l.base.Error(err, "Error sending log packet")
	}
}

func (l *logger) writePropertyList(m map[string]interface{}, keysAndValues []interface{}) {
	idx := 0
	for ; idx + 1 < len(keysAndValues); idx += 2 {
		var key string
		if s, ok := keysAndValues[idx].(string); ok {
			key = s
		} else {
			key = fmt.Sprintf("%v", keysAndValues[idx])
		}

		_, hasExisting := m[key]
		if hasExisting {
			continue
		}

		m[key] = keysAndValues[idx+1]
	}

	if idx < len(keysAndValues) {
		l.base.Info("Unexpected extra logging value", "value", keysAndValues[idx])
	}
}

func (l *logger) writeLogProperties(m map[string]interface{}, keysAndValues []interface{}) {
	l.writePropertyList(m, l.values)
	l.writePropertyList(m, keysAndValues)
}

func (l *logger) Enabled() bool {
	return true
}

func (l *logger) Info(msg string, keysAndValues ...interface{}) {
	payload := map[string]interface{}{
		"textPayload": msg,
	}
	l.writeLogProperties(payload, keysAndValues)
	l.sendLog(payload)
}

func (l *logger) Error(err error, msg string, keysAndValues ...interface{}) {
	payload := map[string]interface{}{
		"textPayload": msg,
		"error":       err.Error(),
	}
	l.writeLogProperties(payload, keysAndValues)
	l.sendLog(payload)
}

func (l *logger) V(level int) logr.Logger {
	return l
}

func (l *logger) WithValues(keysAndValues ...interface{}) logr.Logger {
	numPairs := len(keysAndValues) / 2
	numItems := numPairs * 2

	if numItems != len(keysAndValues) {
		l.base.Info("Bad properties sent with log",
			"extraValues", keysAndValues[numItems:])
	}

	values := make([]interface{}, len(l.values)+numItems)
	offset := len(l.values)
	copy(values[:offset], l.values)
	copy(values[offset:], keysAndValues[:numItems])

	n := &logger{
		communicator: l.communicator,
		base:         l.base,
		name:         l.name,
		values:       values,
	}
	return n
}

func (l *logger) WithName(name string) logr.Logger {
	name = fmt.Sprintf("%s.%s", l.name, name)
	n := &logger{
		communicator: l.communicator,
		base:         l.base,
		name:         name,
		values:       l.values,
	}
	return n
}

// AddLogger adds logger support to the communicator.
//
// This must be called before Run() is called on the communicator.
func AddLogger(c *Communicator, parent logr.Logger) logr.Logger {
	c.AddCapability("log")
	c.AddCapability("error-report")

	return &logger{
		communicator: c,
		base:         parent,
		name:         "",
		values:       nil,
	}
}
