// This package contains helpers to make the logging awesome. In particular,
// here you can find a pretty logger and a log tee, which outputs logs to
// multiple sources.
package log

import (
	"bufio"
	"bytes"
	"crypto/md5"
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

// LogClose closes a Closer and logs any errors which occur.
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
	nameColours := []func(args interface{}) aurora.Value{
		aurora.Cyan,
		aurora.Blue,
		aurora.Yellow,
		aurora.Magenta,
		aurora.Red,
		aurora.Green,
	}

	now := time.Now().UTC().Format(time.RFC3339)[:20]

	buf := bytes.NewBuffer(make([]byte, 0, 160))

	prefix := bytes.NewBuffer(make([]byte, 0, 30))
	prefix.WriteString(now)
	if len(e.Name) > 0 {
		prefix.WriteByte(' ')
		prefix.WriteString(e.Name)
	}

	prefix.WriteByte(' ')

	l := prefix.Len()
	for l < 30 {
		prefix.WriteByte(' ')
		l += 1
	}

	colour := aurora.White
	if len(e.Name) > 0 {
		nameHash := md5.Sum(([]byte)(e.Name))
		colour = nameColours[int(nameHash[0])%len(nameColours)]
	}

	buf.WriteString(colour(prefix.String()).String())

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
