FROM golang:1.13 as build

WORKDIR /workspace
ENV TASKCLUSTER_VERSION=v37.3.0

COPY go.mod go.sum ./
RUN go mod download

COPY internal/ internal/
COPY cmd/ cmd/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o multistage-docker-worker ./cmd/multistage-docker-worker &&\
	curl -o start-worker -L "https://github.com/taskcluster/taskcluster/releases/download/$TASKCLUSTER_VERSION/start-worker-linux-amd64" &&\
    chmod +x start-worker

FROM gcr.io/distroless/static
ADD etc/worker-runner.yaml /worker-runner.yaml
COPY --from=build /workspace/start-worker /workspace/multistage-docker-worker /
ENTRYPOINT ["/start-worker", "/worker-runner.yaml"]
