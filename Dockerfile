FROM golang:1.25.3-trixie
COPY go.* /go/src/github.com/alphauslabs/oops/
COPY *.go /go/src/github.com/alphauslabs/oops/
WORKDIR /go/src/github.com/alphauslabs/oops/
RUN GO111MODULE=on CGO_ENABLED=0 GOOS=linux go build -v -a -installsuffix cgo -o oops .

FROM ubuntu:24.04
WORKDIR /oops/
COPY --from=0 /go/src/github.com/alphauslabs/oops .
COPY examples/ /oops/examples/
ENTRYPOINT ["/oops/oops"]
CMD ["run"]
