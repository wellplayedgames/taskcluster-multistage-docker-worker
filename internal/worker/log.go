package worker

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

func copyToLog(r io.Reader, log logr.Logger) error {
	rd := bufio.NewReader(r)

	for {
		line, _, err := rd.ReadLine()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		log.Info(string(line))
	}
}

func prettyValue(v interface{}) string {
	s := fmt.Sprintf("%v", v)
	by, _ := json.Marshal(&s)
	return string(by)
}

func fancyLog(e genericr.Entry)  string {
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
