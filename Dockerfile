FROM golang:1.16 as builder

WORKDIR /go/src/github.com/agilestacks/auth-operator
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY cmd/ cmd/
RUN go build -ldflags "-linkmode external -extldflags -static" -o auth-operator github.com/agilestacks/auth-operator/cmd/manager

FROM alpine:3.13
COPY --from=builder /go/src/github.com/agilestacks/auth-operator/auth-operator /bin/
ENTRYPOINT ["/bin/auth-operator"]
