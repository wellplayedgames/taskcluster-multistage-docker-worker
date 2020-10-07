package workerproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/go-logr/logr"
	"io"
	"strings"
)

type protoPacket struct {
	Type string `json:"type"`
}

// ProtoHandler is used by the low-level transport code to handle messages
// from an input stream.
type ProtoHandler = func(packetType string, msg []byte) error

// Parse is a low-level function used to parse the input stream for a workerproto.
func Parse(log logr.Logger, r io.Reader, f ProtoHandler) error {
	rd := bufio.NewReader(r)

	for {
		lineBytes, _, err := rd.ReadLine()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		line := strings.TrimSpace(string(lineBytes))
		if len(line) == 0 {
			continue
		}

		if line[0] == '~' && line[1] == '{' {
			// This might be a workerproto message.
			var packet protoPacket
			pr := strings.NewReader(line[1:])
			d := json.NewDecoder(pr)

			err := d.Decode(&packet)
			if err == nil && packet.Type != "" {
				packetEnd := len(line) - pr.Len()
				p := line[1:packetEnd]
				err = f(packet.Type, []byte(p))
				if err != nil {
					return err
				}

				line = line[packetEnd:]
				if line == "" {
					continue
				}
			}
		}

		log.Info(line)
	}
}

// Write is a low-level transport function which writes a single workerproto
// message to an output stream.
func Write(w io.Writer, packet interface{}) error {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)

	buf.WriteByte('~')
	if err := e.Encode(packet); err != nil {
		return err
	}
	buf.WriteByte('\n')

	_, err := w.Write(buf.Bytes())
	return err
}
