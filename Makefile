build_tag := dev
image_name := kostyaesmukov/smtp_to_telegram
image_build_tag := $(image_name):$(build_tag)
image_push_tag := $(image_name):latest

build:
	docker build --pull --force-rm \
		--build-arg ST_VERSION=`git describe --tags --always` \
		-t ${image_build_tag} .

test-fmt:
	docker run \
		--entrypoint=sh \
		--rm ${image_build_tag} \
		-c "test -z `go fmt`"

test:
	@# go wants gcc, `CGO_ENABLED=0` fixes that.
	@# See: https://github.com/golang/go/issues/26988
	docker run \
		--entrypoint=sh -u 0:0 \
		--rm ${image_build_tag} \
		-c "CGO_ENABLED=0 go test"

test-help:
	docker run \
		--rm ${image_build_tag} \
		--help 2>&1 | grep -q 'A small program which listens'

push:
	docker tag ${image_build_tag} ${image_push_tag}
	docker push ${image_push_tag}

clean:
	docker rmi ${image_build_tag} || true
