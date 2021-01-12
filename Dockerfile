FROM golang:1.13 as build

WORKDIR /workspace
ENV TASKCLUSTER_VERSION=v40.0.3
ENV DOCKER_CREDENTIAL_GCR_VERSION=2.0.2

COPY go.mod go.sum ./
RUN go mod download

COPY internal/ internal/
COPY cmd/ cmd/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o multistage-docker-worker ./cmd/multistage-docker-worker &&\
	curl -o start-worker -L "https://github.com/taskcluster/taskcluster/releases/download/$TASKCLUSTER_VERSION/start-worker-linux-amd64" &&\
    chmod +x start-worker &&\
    curl -o docker-credential-gcr.tar.gz -L "https://github.com/GoogleCloudPlatform/docker-credential-gcr/releases/download/v$DOCKER_CREDENTIAL_GCR_VERSION/docker-credential-gcr_linux_amd64-$DOCKER_CREDENTIAL_GCR_VERSION.tar.gz" &&\
    tar xvf docker-credential-gcr.tar.gz &&\
    mkdir /workspace/docker-worker

FROM gcr.io/distroless/static
ADD etc/worker-runner.yaml /worker-runner.yaml
COPY --from=build /workspace/docker-worker /etc/docker-worker
COPY --from=build /workspace/start-worker /workspace/multistage-docker-worker /
COPY --from=build /workspace/docker-credential-gcr /usr/bin/
ENTRYPOINT ["/start-worker", "/worker-runner.yaml"]
