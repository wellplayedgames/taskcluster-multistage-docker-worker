package log

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/go-logr/logr"
	"github.com/logrusorgru/aurora/v3"
	"github.com/wojas/genericr"
)

// CopyToLogPrefix splits a reader by newlines and emits log entries with a prefix.
func CopyToLogPrefix(log logr.Logger, r io.Reader, prefix string) error {
	rd := bufio.NewReader(r)

	for {
		line, _, err := rd.ReadLine()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		s := string(line)

		if prefix != "" {
			s = prefix + s
		}

		log.Info(s)
	}
}

// CopyToLog splits a reader by newlines and emits log entries.
func CopyToLog(log logr.Logger, r io.Reader) error {
	return CopyToLogPrefix(log, r, "")
}

// CopyToLogPrefixNoError emits log entries from a reader with a prefix. Any error during read will be logged.
func CopyToLogPrefixNoError(log logr.Logger, r io.Reader, prefix string) {
	err := CopyToLogPrefix(log, r, prefix)
	if err != nil {
		log.Error(err, "Error copying log")
	}
}

// CopyToLogNoError emits log entries from a reader. Any error during read will be logged.
func CopyToLogNoError(log logr.Logger, r io.Reader) {
	CopyToLogPrefixNoError(log, r, "")
}

// LogClose closes a Closer and loggers any errors which occur.
func LogClose(log logr.Logger, c io.Closer, msg string) {
	err := c.Close()
	if err != nil {
		log.Error(err, msg)
	}
}

func prettyValue(v interface{}) string {
	if a, ok := v.([]string); ok {
		if a == nil {
			return "[]"
		} else {
			by, _ := json.Marshal(&a)
			return string(by)
		}
	}

	s := fmt.Sprintf("%v", v)
	by, _ := json.Marshal(&s)
	return string(by)
}

// FancyLog is a logging function for genericr which prints a format which is
// nice to use for CI output.
func FancyLog(e genericr.Entry)  string {
	now := time.Now().UTC().Format(time.RFC3339)[:20]
	buf := bytes.NewBuffer(make([]byte, 0, 160))
	buf.WriteString(now)

	if len(e.Name) > 0 {
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
	}

	buf.WriteByte(' ')

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
