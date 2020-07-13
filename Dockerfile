FROM golang:1.14.4
COPY go.* /go/src/github.com/flowerinthenight/oops/
COPY *.go /go/src/github.com/flowerinthenight/oops/
WORKDIR /go/src/github.com/flowerinthenight/oops/
RUN GO111MODULE=on CGO_ENABLED=0 GOOS=linux go build -v -a -installsuffix cgo -o oops .

FROM ubuntu:20.04
WORKDIR /oops/
COPY --from=0 /go/src/github.com/flowerinthenight/oops .
COPY examples/ /oops/examples/
ENTRYPOINT ["/oops/oops"]
CMD ["run"]
