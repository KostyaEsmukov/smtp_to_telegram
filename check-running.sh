#!/bin/sh

set -eu

nc -w 3 -z 127.0.0.1 2525 && nc -w 5 -z api.telegram.org 443 || exit 1
