#!/bin/bash

set -eou pipefail   

[[ $# -lt 1 ]] && echo "Usage: kurun gofiles... [arguments...]" && exit 1

gofiles=$(echo "$@" | awk 'BEGIN{RS=" ";}/\.go/{print $1}')
arguments=$(echo "$@" | awk 'BEGIN{RS=" ";}!/\.go/{print $1}')

mkdir -p /tmp/kurun

GOOS=linux go build -o /tmp/kurun/main $gofiles

cat <<EOF > /tmp/kurun/Dockerfile
FROM alpine
ADD main /
EOF

docker build -t kurun /tmp/kurun

kubectl run kurun -it --image kurun --quiet --image-pull-policy=IfNotPresent --restart=Never --rm --command -- sh -c "sleep 1 && /main $arguments"
