#!/usr/bin/env bash

# docker build -f Dockerfile.server --progress=plain -t tunnel .

# kind load docker-image tunnel

APISERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
CERT=$(kubectl config view --raw -o json | jq -r '.users[0].user["client-certificate-data"]' | base64 -d)
KEY=$(kubectl config view --raw -o json | jq -r '.users[0].user["client-key-data"]' | base64 -d)
CA=$(kubectl config view --raw -o json | jq -r '.clusters[0].cluster["certificate-authority-data"]' | base64 -d)

# curl -v "$APISERVER/api/v1/namespaces/default/pods/tunnel:80/proxy/log-generator/state" --cert <(echo "$CERT") --key <(echo "$KEY") --cacert <(echo "$CA")

curl -v "$APISERVER/api/v1/namespaces/default/services/https:tunnel-service:443/proxy/$1" --cert <(echo "$CERT") --key <(echo "$KEY") --cacert <(echo "$CA")