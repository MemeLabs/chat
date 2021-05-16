FROM golang:alpine AS builder
RUN apk --no-cache add \
  ca-certificates \
  build-base

ENV GO111MODULE=on \
    CGO_ENABLED=1

WORKDIR /build

COPY go.mod .
COPY go.sum .
RUN go mod download
RUN go mod verify

COPY *.go ./
RUN go build -o chat -a -ldflags '-extldflags "-static"' .
WORKDIR /dist
RUN cp /build/chat .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /dist/chat /
COPY db-init.sql /

EXPOSE 9998

ENTRYPOINT ["/chat"]
