package worker

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	tcclient "github.com/taskcluster/taskcluster/v41/clients/client-go"
	"github.com/taskcluster/taskcluster/v41/clients/client-go/tcqueue"
)

func createRedirectArtifact(queue *tcqueue.Queue, claim *tcqueue.TaskClaim, name, url, contentType string, expires time.Time) error {
	createReq := tcqueue.RedirectArtifactRequest{
		ContentType: contentType,
		Expires:     tcclient.Time(expires),
		StorageType: "reference",
		URL:         url,
	}

	reqBy, err := json.Marshal(&createReq)
	if err != nil {
		return err
	}

	req := tcqueue.PostArtifactRequest(reqBy)
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	_, err = queue.CreateArtifact(claim.Status.TaskID, runIdStr, name, &req)
	return err
}

func createLinkArtifact(queue *tcqueue.Queue, claim *tcqueue.TaskClaim, name, contentType, target string, expires time.Time) error {
	createReq := tcqueue.LinkArtifactRequest{
		Expires:     tcclient.Time(expires),
		StorageType: "link",
		ContentType: contentType,
		Artifact:    target,
	}

	reqBy, err := json.Marshal(&createReq)
	if err != nil {
		return err
	}

	req := tcqueue.PostArtifactRequest(reqBy)
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	_, err = queue.CreateArtifact(claim.Status.TaskID, runIdStr, name, &req)
	return err
}

func createS3Artifact(queue *tcqueue.Queue, claim *tcqueue.TaskClaim, name, contentType string, expires time.Time, contentLen int, r io.Reader) error {
	createReq := tcqueue.S3ArtifactRequest{
		ContentType: contentType,
		Expires:     tcclient.Time(expires),
		StorageType: "s3",
	}

	reqBy, err := json.Marshal(&createReq)
	if err != nil {
		return err
	}

	req := tcqueue.PostArtifactRequest(reqBy)
	runIdStr := strconv.FormatInt(claim.RunID, 10)
	respBy, err := queue.CreateArtifact(claim.Status.TaskID, runIdStr, name, &req)
	if err != nil {
		return err
	}

	var resp tcqueue.S3ArtifactResponse
	if err := json.Unmarshal(*respBy, &resp); err != nil {
		return err
	}

	putReq, err := http.NewRequest(http.MethodPut, resp.PutURL, r)
	if err != nil {
		return err
	}

	putReq.Header.Set("Content-Type", resp.ContentType)
	putReq.ContentLength = int64(contentLen)

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode >= 300 {
		bodyText, _ := ioutil.ReadAll(putResp.Body)

		return fmt.Errorf("unexpected status code creating artifact: %d: %s", putResp.StatusCode, string(bodyText))
	}

	return nil
}
