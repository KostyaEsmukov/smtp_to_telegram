#!/bin/sh

set -eu

CMD nc -z 127.0.0.1 2525 && nc -z api.telegram.org 443 && exit 0 || exit 1
