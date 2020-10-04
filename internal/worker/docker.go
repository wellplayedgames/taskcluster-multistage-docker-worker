package worker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/go-logr/logr"
	"github.com/logrusorgru/aurora/v3"
)

func parseDockerLogs(r io.Reader, w io.Writer) error {
	max := 4096
	buf := make([]byte, max)

	for {
		_, err := io.ReadFull(r, buf[:8])
		if err != nil {
			return err
		}

		streamType := buf[0]
		header := "\033[0m"
		if streamType == 2 {
			header = fmt.Sprintf("\033[%sm", aurora.RedFg.Nos(true))
		}

		_, err = w.Write([]byte(header))
		if err != nil {
			return err
		}

		n := int(binary.BigEndian.Uint32(buf[4:8]))
		for n > 0 {
			toRead := int(n)
			if toRead > max {
				toRead = max
			}

			_, err := io.ReadFull(r, buf[:toRead])
			if err != nil {
				return err
			}


			_, err = w.Write(buf[:toRead])
			if err != nil {
				return err
			}

			n -= toRead
		}
	}
}

func dockerLogs(ctx context.Context, log logr.Logger, cl client.APIClient, id string) error {
	rd, err := cl.ContainerLogs(ctx, id, types.ContainerLogsOptions{
		Follow:     true,
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return err
	}
	defer rd.Close()

	pr, pw := io.Pipe()

	go func() {
		err := fmt.Errorf("panic")
		defer func() {
			pw.CloseWithError(err)
		}()
		err = parseDockerLogs(rd, pw)
	}()

	return copyToLog(pr, log)
}

func canonicalImage(image string) string {
	repo := image
	tagOffset := strings.IndexRune(image, ':')
	if tagOffset >= 0 {
		repo = image[:tagOffset]
	}

	numSlashes := strings.Count(repo, "/")
	switch numSlashes {
	case 0:
		return "docker.io/library/" + image

	case 1:
		return "docker.io/" + image

	default:
		return image
	}
}

func dockerPull(ctx context.Context, log logr.Logger, cl client.APIClient, image string) (err error) {
	r, err := cl.ImagePull(ctx, canonicalImage(image), types.ImagePullOptions{})
	if err != nil {
		return
	}
	defer func() {
		cerr := r.Close()
		if err == nil {
			err = cerr
		}
	}()

	br := bufio.NewReader(r)

	for {
		var status struct {
			Status string `json:"status"`
			ID     string `json:"id"`
		}

		line, _, lerr := br.ReadLine()
		if lerr == io.EOF {
			return
		} else if lerr != nil {
			err = lerr
			return
		}

		lerr = json.Unmarshal(line, &status)
		if lerr == nil {
			if !strings.HasSuffix(status.Status, "ing") {
				if status.ID == "" {
					log.Info(status.Status)
				} else {
					log.Info(fmt.Sprintf("%s: %s", status.ID, status.Status))
				}
			}
		} else {
			log.Info(string(line))
		}
	}
}
