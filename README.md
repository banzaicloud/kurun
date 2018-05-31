# kurun
Just like `go run main.go` but executed inside Kubernetes with one command.

### Prerequisites

A Kubernetes cluster, where you have access to the image storage of the cluster itself, for example:
- Docker for Mac Edge with Kubernetes enabled
- Minikube with the Registry addon enabled

### Installation

#### Bash version
```bash
curl https://raw.githubusercontent.com/banzaicloud/kurun/master/kurun > /usr/local/bin/kurun && chmod +x /usr/local/bin/kurun
```

#### Go version
```bash
go get github.com/banzaicloud/kurun
```

### Usage

```bash
kurun test.go arg1 arg2 arg3
```


### `kurun` is like `go run`

The `go run` command is a really convenient CLI subcommand for executing `Golang` code during the development phase. A lot of our applications are making calls to the Kubernetes API and we needed a quick utility to execute the **Go code inside Kubernetes** very quickly. That's why we have written `kurun`, a dirty little bash utility, to execute Go code inside Kubernetes with a oneliner: 

`kurun main.go` 

It's that easy.

To see how you can leverage `kurun` let’s try checking it out with a small example which lists all nodes in your Kubernetes cluster:

```
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

```
git clone git@github.com:banzaicloud/kurun.git
cd kurun
# Download the dependencies, this is just a one-time step to get the k8s libraries
go get ./...
./kurun test.go
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

For some more details and examples please read this [post](https://banzaicloud.com/blog/kurun).
