FROM golang:1.11-alpine3.8

RUN apk add --no-cache git ca-certificates \
    && apk add dep --no-cache --repository=http://dl-cdn.alpinelinux.org/alpine/edge/community

WORKDIR $GOPATH/src/github.com/KostyaEsmukov/smtp_to_telegram

COPY . .

RUN dep ensure \
    && chown -R daemon $GOPATH/pkg/dep

# The image should be built with
# --build-arg ST_VERSION=`git describe --tags --always`
ARG ST_VERSION
RUN if [ ! -z "$ST_VERSION" ]; then sed -i "s/UNKNOWN_RELEASE/${ST_VERSION}/g" smtp_to_telegram.go; fi

RUN go build \
        -ldflags "-s -w" \
        -o smtp_to_telegram smtp_to_telegram.go

RUN cp ./smtp_to_telegram /smtp_to_telegram

USER daemon

ENV ST_SMTP_LISTEN "0.0.0.0:2525"
EXPOSE 2525

ENTRYPOINT ["/smtp_to_telegram"]
