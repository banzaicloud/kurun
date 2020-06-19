# kurun

- Just like `go run main.go` but executed inside Kubernetes with one command.
- Just like `kubectl port-forward ...` just the other way around!
- Just like `kubectl apply -f pod.yaml` but images are built from local source code.

### Synopsis

```
Usage:
  kurun [command]

Available Commands:
  apply        Just like `kubectl apply -f pod.yaml` but images are built from local source code.
  help         Help about any command
  port-forward Just like `kubectl port-forward ...`, just the other way around!
  run          Just like `go run main.go` but executed inside Kubernetes with one command.

Flags:
  -h, --help               help for kurun
      --namespace string   Namespace to use for the Pod/Service (default "default")

Use "kurun [command] --help" for more information about a command.
```

### Prerequisites

- A Kubernetes cluster, where you have access to the image storage of the cluster itself, for example:
	- Docker for Mac Edge with Kubernetes enabled
	- Minikube with the Registry addon enabled
- kubectl
- [inlets CLI](https://github.com/alexellis/inlets#install-the-cli) (for `port-forward` only)

### Installation

#### Brew version
```bash
brew install banzaicloud/tap/kurun
```

#### Go version
```bash
go get github.com/banzaicloud/kurun
```

#### Binaries under releases

https://github.com/banzaicloud/kurun/releases

### Usage

```bash
kurun run test.go arg1 arg2 arg3
```

```bash
kurun port-forward localhost:4443
```

```bash
kurun apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: myapp
spec:
  containers:
  - image: kurun://cmd/myapp/main.go
    name: myapp
EOF
```

### `kurun` is like `go run` to Kubernetes

The `go run` command is a convenient CLI subcommand for executing `Golang` code during the development phase. A lot of our applications are making calls to the Kubernetes API and we needed a quick utility to execute the **Go code inside Kubernetes** very quickly. That's why we have written `kurun`, a dirty little bash utility, to execute Go code inside Kubernetes with a oneliner: 

`kurun run main.go` 

It's that easy.

To see how you can leverage `kurun` let’s try checking it out with a small example which lists all nodes in your Kubernetes cluster:

```go
package main

import (
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	fmt.Println(os.Args)

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	nodes, err := client.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		panic(err)
	}

	fmt.Println("List of Kubernetes nodes:")
	for _, node := range nodes.Items {
		fmt.Printf("- %s - %s\n", node.Name, node.Labels)
	}
}
```

Execute the following commands in the CLI, make sure your `kubectl` points to the cluster you would like to use:

```bash
git clone git@github.com:banzaicloud/kurun.git
cd kurun
# Download the dependencies, this is just a one-time step to get the k8s libraries
go get ./...
./kurun run example/test.go
Sending build context to Docker daemon  31.05MB
Step 1/2 : FROM alpine
 ---> 3fd9065eaf02
Step 2/2 : ADD main /
 ---> 0f4ee24ec5ea
Successfully built 0f4ee24ec5ea
Successfully tagged kurun:latest
[/main]
List of Kubernetes nodes:
- docker-for-desktop - map[beta.kubernetes.io/arch:amd64 beta.kubernetes.io/os:linux kubernetes.io/hostname:docker-for-desktop node-role.kubernetes.io/master:]
```

### `kurun` is like `kubectl port-forward` into Kubernetes (and not out from!)

`kurun` is capable of port forwarding your local application into a Kubernetes cluster with the help of a few tricks, it uses [inlets](https://github.com/alexellis/inlets) and `kubectl port-forward` to achieve this. This is extremely useful for rapid development of Kubernetes admission webhooks for example.

Start a Python SimpleHTTPServer on port 4443 on your machine:

```bash
$ python -m SimpleHTTPServer 4443
```

In another terminal session proxy this application into Kubernetes under the Service name `python-server`:

```bash
kurun port-forward --servicename python-server localhost:4443
```

In a third terminal session proxy this application into Kubernetes under the Service name `python-server`:

```bash
kubectl run alpine --rm --image alpine -it
If you don't see a command prompt, try pressing enter.

/ # apk add curl
fetch http://dl-cdn.alpinelinux.org/alpine/v3.10/main/x86_64/APKINDEX.tar.gz
fetch http://dl-cdn.alpinelinux.org/alpine/v3.10/community/x86_64/APKINDEX.tar.gz
(1/4) Installing ca-certificates (20190108-r0)
(2/4) Installing nghttp2-libs (1.38.0-r0)
(3/4) Installing libcurl (7.65.1-r0)
(4/4) Installing curl (7.65.1-r0)
Executing busybox-1.30.1-r2.trigger
Executing ca-certificates-20190108-r0.trigger
OK: 7 MiB in 18 packages

/ # curl http://python-server
<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 3.2 Final//EN"><html>
<title>Directory listing for /</title>
<body>
<h2>Directory listing for /</h2>
<hr>
<ul>
<li><a href=".git/">.git/</a>
<li><a href=".gitignore">.gitignore</a>
<li><a href=".goreleaser.yml">.goreleaser.yml</a>
<li><a href="dist/">dist/</a>
<li><a href="example/">example/</a>
<li><a href="go.mod">go.mod</a>
<li><a href="go.sum">go.sum</a>
<li><a href="kurun">kurun</a>
<li><a href="kurun.go">kurun.go</a>
<li><a href="LICENSE">LICENSE</a>
<li><a href="README.md">README.md</a>
</ul>
<hr>
</body>
</html>
```

It's also possible to proxy HTTPS services from localhost (just add the `https://` scheme prefix to the URL):

```bash
kurun port-forward --servicename pipeline https://localhost:9090
```

The above will end up as a plaintext service inside Kubernetes.

If you need TLS there as well you have to provide the TLS type Kubernetes Secret name to `kurun`:

```bash
kubectl create secret tls pipeline-cert --cert "tls.crt" --key "tls.key"
kurun port-forward --servicename pipeline https://localhost:9090 --tlssecret pipeline-cert
```

For some more details and examples please read this [post](https://banzaicloud.com/blog/kurun).
