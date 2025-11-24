FROM golang:1.25.4-alpine3.22 AS builder
WORKDIR /app
COPY . /app

RUN go build

FROM alpine:3.22

COPY --from=builder /app/external-dns-inwx-webhook /
ENTRYPOINT ["/external-dns-inwx-webhook"]
