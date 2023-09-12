# Build the manager binary
FROM registry.access.redhat.com/ubi8/go-toolset:1.19.10-10 as builder

WORKDIR /workspace
# Copy the Go modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go sources
COPY main.go main.go
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
USER root
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager main.go

FROM registry.access.redhat.com/ubi8/ubi-minimal:8.7
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
