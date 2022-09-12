FROM golang:1.19-alpine as builder
WORKDIR /go/src/github.com/agilestacks/auth-operator
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY cmd/ cmd/
RUN go build -o auth-operator github.com/agilestacks/auth-operator/cmd/manager

FROM alpine:3.16
COPY --from=builder /go/src/github.com/agilestacks/auth-operator/auth-operator /bin/
ENTRYPOINT ["/bin/auth-operator"]
