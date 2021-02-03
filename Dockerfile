FROM golang:1.15.7
COPY go.* /go/src/github.com/alphauslabs/oops/
COPY *.go /go/src/github.com/alphauslabs/oops/
WORKDIR /go/src/github.com/alphauslabs/oops/
RUN GO111MODULE=on CGO_ENABLED=0 GOOS=linux go build -v -a -installsuffix cgo -o oops .

FROM ubuntu:20.04
WORKDIR /oops/
COPY --from=0 /go/src/github.com/alphauslabs/oops .
COPY examples/ /oops/examples/
ENTRYPOINT ["/oops/oops"]
CMD ["run"]
