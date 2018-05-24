# kurun
Just like `go run main.go` but executed inside Kubernetes with one command.

### Prerequisites

A Kubernetes cluster, where you have access to the image storage of the cluster itself, for example:
- Docker for Mac Edge with Kubernetes enabled
- Minikube with the Registry addon enabled

### Installation
```bash
curl https://raw.githubusercontent.com/banzaicloud/kurun/master/kurun > /usr/local/bin/kurun && chmod +x /usr/local/bin/kurun
```

### Usage
```bash
kurun test.go arg1 arg2 arg3
```

For more details and examples please read this [post](https://banzaicloud.com/blog/kurun)
