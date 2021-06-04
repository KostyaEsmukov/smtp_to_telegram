FROM golang:1.16-alpine3.13 AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY . .

# The image should be built with
# --build-arg ST_VERSION=`git describe --tags --always`
ARG ST_VERSION
RUN CGO_ENABLED=0 GOOS=linux go build \
        -ldflags "-s -w \
            -X main.Version=${ST_VERSION:-UNKNOWN_RELEASE}" \
        -a -o smtp_to_telegram





FROM alpine:3.13

RUN apk add --no-cache ca-certificates

COPY --from=builder /app/smtp_to_telegram /smtp_to_telegram
COPY check-running.sh /check-running.sh

USER daemon

ENV ST_SMTP_LISTEN "0.0.0.0:2525"
EXPOSE 2525

HEALTHCHECK CMD /check-running.sh || exit 1

ENTRYPOINT ["/smtp_to_telegram"]
