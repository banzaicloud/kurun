package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8sYaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const kurunSchemaPrefix = "kurun://"

var serviceAccount string
var servicePort int
var serviceName string
var tlsSecret string
var podEnv []string
var namespace string
var labels []string
var files []string
var localPort int

func getKubeConfig() (*rest.Config, error) {
	// If an env variable is specified with the config locaiton, use that
	if len(os.Getenv("KUBECONFIG")) > 0 {
		return clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	}
	// If no explicit location, try the in-cluster config
	if c, err := rest.InClusterConfig(); err == nil {
		return c, nil
	}
	// If no in-cluster config, try the default location in the user's home directory
	if usr, err := user.Current(); err == nil {
		if c, err := clientcmd.BuildConfigFromFlags(
			"", filepath.Join(usr.HomeDir, ".kube", "config")); err == nil {
			return c, nil
		}
	}

	return nil, fmt.Errorf("could not locate a kubeconfig")
}

func runKubectl(args []string) error {
	args = append(args, fmt.Sprintf("--namespace=%s", namespace))

	cmd := exec.Command("kubectl", args...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	return cmd.Run()
}

// Detect if the cluster is a KinD cluster, because in case of that
// we need to load the Docker images into the cluster.
func isKindCluster() (bool, error) {
	buffer := bytes.NewBuffer(nil)

	kubectlConfigCommand := exec.Command("kubectl", "config", "current-context")
	kubectlConfigCommand.Stderr = os.Stderr
	kubectlConfigCommand.Stdout = buffer

	if err := kubectlConfigCommand.Run(); err != nil {
		return false, err
	}

	kubeContext := strings.TrimSuffix(buffer.String(), "\n")

	return kubeContext == "kubernetes-admin@kind" || kubeContext == "kind-kind", nil
}

var runCmd = &cobra.Command{
	Use:   "run [flags] -- gofiles... [arguments...]",
	Short: "Just like `go run main.go` but executed inside Kubernetes with one command.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {

		var gofiles []string
		var finalArguments []string

		for _, arg := range args {
			if strings.HasSuffix(arg, ".go") {
				gofiles = append(gofiles, arg)
			} else {
				finalArguments = append(finalArguments, arg)
			}
		}

		image, err := buildImage(gofiles)
		if err != nil {
			return err
		}

		mode := "-i"
		if fileInfo, _ := os.Stdout.Stat(); (fileInfo.Mode() & os.ModeCharDevice) != 0 {
			mode += "t"
		}

		kubectlArgs := []string{
			"run", strings.Split(image, ":")[0],
			mode,
			"--image=docker.io/library/" + image,
			"--quiet",
			"--image-pull-policy=IfNotPresent",
			"--restart=Never",
			"--rm",
			"--limits=cpu=100m,memory=128Mi",
		}

		if serviceAccount != "" {
			kubectlArgs = append(kubectlArgs, fmt.Sprintf("--serviceaccount=%s", serviceAccount))
		}

		if podEnv != nil {
			for _, e := range podEnv {
				kubectlArgs = append(kubectlArgs, fmt.Sprintf("--env=%s", e))
			}
		}

		kubectlArgs = append(kubectlArgs, fmt.Sprintf("--namespace=%s", namespace), "--command", "--", "sh", "-c", fmt.Sprintf("sleep 1 && /main %s", strings.Join(finalArguments[:], " ")))
		kubectlCommand := exec.Command("kubectl", kubectlArgs...)
		kubectlCommand.Stdin = os.Stdin
		kubectlCommand.Stderr = os.Stderr
		kubectlCommand.Stdout = os.Stdout

		if err := kubectlCommand.Run(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}

		return nil
	},
}

var portForwardCmd = &cobra.Command{
	Use:     "port-forward [flags] upstream",
	Short:   "Just like `kubectl port-forward ...`, just the other way around!",
	Example: "kurun port-forward --namespace apps localhost:4443",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {

		tokenBytes := make([]byte, 16)
		_, err := rand.New(rand.NewSource(time.Now().UnixNano())).Read(tokenBytes)
		if err != nil {
			return err
		}

		token := base64.RawStdEncoding.EncodeToString(tokenBytes)

		deploymentName := serviceName
		if deploymentName != "kurun" {
			deploymentName += "-kurun"
		}

		labelsMap := map[string]string{
			"app": deploymentName,
		}

		for _, label := range labels {
			labelPair := strings.Split(label, "=")
			if len(labelPair) == 2 {
				labelsMap[labelPair[0]] = labelPair[1]
			}
		}

		kubeConfig, err := getKubeConfig()
		if err != nil {
			return err
		}

		kubeClient, err := kubernetes.NewForConfig(kubeConfig)
		if err != nil {
			return err
		}

		kubeService, err := kubeClient.CoreV1().Services(namespace).Get(serviceName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				kubeService = nil
			} else {
				return err
			}
		}

		if kubeService != nil {
			for k, v := range kubeService.Spec.Selector {
				labelsMap[k] = v
			}
		}

		done := make(chan bool, 1)

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

		go func() {
			<-signals
			fmt.Println("\rCtrl+C pressed, exiting")

			kubectlArgs := []string{
				"delete",
				"deployment",
				deploymentName,
			}

			if err := runKubectl(kubectlArgs); err != nil {
				fmt.Println("failed to delete deployment", err.Error())
			}

			/////

			if kubeService == nil {
				kubectlArgs = []string{
					"delete",
					"service",
					serviceName,
				}

				if err := runKubectl(kubectlArgs); err != nil {
					fmt.Println("failed to delete service", err.Error())
				}
			}

			done <- true
		}()

		ports := []corev1.ContainerPort{
			{
				Name:          "inlets",
				ContainerPort: 8444,
			},
		}

		if kubeService != nil {
			firstServicePort := kubeService.Spec.Ports[0]

			if firstServicePort.TargetPort.String() != "" || firstServicePort.TargetPort.IntValue() != 0 {
				if firstServicePort.TargetPort.Type == intstr.Int {
					ports[0].ContainerPort = int32(firstServicePort.TargetPort.IntValue())
				} else if firstServicePort.TargetPort.Type == intstr.String {
					ports[0].Name = firstServicePort.TargetPort.String()
				}
			} else {
				ports[0].ContainerPort = firstServicePort.Port
			}
		}

		inletsPort := ports[0].ContainerPort

		if tlsSecret != "" {
			inletsPort++
		}

		containers := []corev1.Container{
			{
				Name:    "inlets-server",
				Image:   "ghcr.io/inlets/inlets:3.0.1",
				Command: []string{"inlets", "server", "-p", fmt.Sprint(inletsPort), "--token", token},
				Ports:   ports,
			},
		}

		volumes := []corev1.Volume{}

		if tlsSecret != "" {

			containers = append(containers, corev1.Container{
				Name:  "ghostunnel",
				Image: "squareup/ghostunnel:v1.5.2",
				Args: []string{
					"server",
					"--target",
					"127.0.0.1:" + fmt.Sprint(inletsPort),
					"--listen",
					"0.0.0.0:" + fmt.Sprint(inletsPort-1),
					"--cert",
					"/etc/tls/tls.crt",
					"--key",
					"/etc/tls/tls.key",
					"--disable-authentication",
				},
				Ports: ports,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      tlsSecret,
						MountPath: "/etc/tls",
					},
				},
			})
			volumes = append(volumes, corev1.Volume{
				Name: tlsSecret,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: tlsSecret,
					},
				},
			})
		}

		deployment := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: namespace,
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: labelsMap,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: labelsMap,
					},
					Spec: corev1.PodSpec{
						Containers: containers,
						Volumes:    volumes,
					},
				},
			},
		}

		_, err = kubeClient.AppsV1().Deployments(namespace).Create(&deployment)
		if err != nil {
			return err
		}

		//////

		kubectlArgs := []string{
			"wait",
			"--for=condition=available",
			"deployment/" + deploymentName,
			"--timeout=60s",
		}

		if err := runKubectl(kubectlArgs); err != nil {
			return err
		}

		//////

		if kubeService == nil {
			kubectlArgs = []string{
				"expose",
				"deployment",
				deploymentName,
				"--name=" + serviceName,
				"--port=" + fmt.Sprint(servicePort),
				"--target-port=8444",
			}

			if err := runKubectl(kubectlArgs); err != nil {
				return err
			}
		}

		////////

		kubectlArgs = []string{
			"port-forward",
			"deployment/" + deploymentName,
			fmt.Sprint(localPort) + ":" + fmt.Sprint(inletsPort),
			fmt.Sprintf("--namespace=%s", namespace),
		}

		kubectlCommand := exec.Command("kubectl", kubectlArgs...)
		kubectlCommand.Stdin = os.Stdin
		kubectlCommand.Stderr = os.Stderr
		kubectlCommand.Stdout = os.Stdout

		if err := kubectlCommand.Start(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}

		fmt.Println("Waiting for kubectl port-forward to build up the connection")
		time.Sleep(2 * time.Second)

		/////

		upstream := args[0]

		inletsCommand := exec.Command("inlets", "client", "--insecure", "--upstream", upstream, "--url", "ws://127.0.0.1:"+fmt.Sprint(localPort), "--token", token)
		inletsCommand.Stderr = os.Stderr
		inletsCommand.Stdout = os.Stdout

		if err := inletsCommand.Start(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}

		<-done

		return nil
	},
}

func buildImage(goFiles []string) (string, error) {
	hash := sha1.New()
	for _, goFile := range goFiles {
		absGoFile, err := filepath.Abs(goFile)
		if err != nil {
			return "", err
		}

		_, err = hash.Write([]byte(absGoFile))
		if err != nil {
			return "", err
		}
	}

	imageTag := fmt.Sprintf("kurun-%x", hash.Sum(nil))
	directory := "/tmp/kurun/" + imageTag

	os.MkdirAll(directory, os.ModePerm)

	goBuildArgs := []string{"build", "-o", directory + "/main"}
	for _, gofile := range goFiles {
		goBuildArgs = append(goBuildArgs, gofile)
	}
	goBuildCommand := exec.Command("go", goBuildArgs...)
	goBuildCommand.Stderr = os.Stderr
	goBuildCommand.Stdout = os.Stdout
	env := os.Environ()
	env = append(env, "GOOS=linux", "CGO_ENABLED=0")
	goBuildCommand.Env = env

	println(goBuildCommand.String())

	if err := goBuildCommand.Run(); err != nil {
		return "", err
	}

	file, err := os.Create(directory + "/Dockerfile")
	if err != nil {
		return "", err
	}
	defer file.Close()

	fmt.Fprintln(file, "FROM alpine")
	fmt.Fprintln(file, "ADD main /")
	fmt.Fprintln(file, "CMD /main")

	dockerBuildCommand := exec.Command("docker", "build", "-t", imageTag, directory)
	dockerBuildCommand.Stderr = os.Stderr
	dockerBuildCommand.Stdout = os.Stdout

	if err := dockerBuildCommand.Run(); err != nil {
		return "", err
	}

	dockerOutput := bytes.NewBuffer(nil)

	dockerInspectCommand := exec.Command("docker", "inspect", imageTag, "-f", "{{.Id}}")
	dockerInspectCommand.Stderr = os.Stderr
	dockerInspectCommand.Stdout = dockerOutput
	if err := dockerInspectCommand.Run(); err != nil {
		return "", err
	}

	imageHash := strings.TrimPrefix(strings.TrimSuffix(dockerOutput.String(), "\n"), "sha256:")

	kindCluster, err := isKindCluster()
	if err != nil {
		return "", err
	}

	fullImageTag := imageTag + ":" + imageHash

	dockerTagCommand := exec.Command("docker", "tag", imageTag, fullImageTag)
	dockerTagCommand.Stderr = os.Stderr
	dockerTagCommand.Stdout = os.Stdout

	if err := dockerTagCommand.Run(); err != nil {
		return "", err
	}

	if kindCluster {
		kindLoadCommand := exec.Command("kind", "load", "docker-image", fullImageTag)
		kindLoadCommand.Stderr = os.Stderr
		kindLoadCommand.Stdout = os.Stdout

		if err := kindLoadCommand.Run(); err != nil {
			return "", err
		}
	}

	return fullImageTag, nil
}

var applyCmd = &cobra.Command{
	Use:   "apply [flags] -f pod.yaml",
	Short: "Just like `kubectl apply -f pod.yaml` but images are built from local source code.",
	RunE: func(cmd *cobra.Command, args []string) error {

		var rawResources [][]byte

		for _, file := range files {

			var manifest io.Reader

			if strings.HasPrefix(file, "http://") || strings.HasPrefix(file, "https://") {
				resp, err := http.Get(file)
				if err != nil {
					return err
				}
				if resp.StatusCode < 200 || resp.StatusCode > 299 {
					return fmt.Errorf("unable to read URL %s, server reported %d", file, resp.StatusCode)
				}

				defer resp.Body.Close()
				manifest = resp.Body

			} else if file == "-" {
				manifest = os.Stdin
			} else {
				var err error
				manifest, err = os.Open(file)
				if err != nil {
					return err
				}
			}

			decoder := k8sYaml.NewYAMLOrJSONDecoder(manifest, 4096)

			var obj *unstructured.Unstructured

			for {
				err := decoder.Decode(&obj)
				if err != nil && err != io.EOF {
					return fmt.Errorf("failed to unmarshal manifest: %s", err)
				}

				if obj == nil {
					break
				}

				var resource interface{}

				switch obj.GetKind() {
				case "Pod":
					pod, err := unstructuredToPod(obj)
					if err != nil {
						return err
					}

					for i, c := range pod.Spec.Containers {
						if strings.HasPrefix(c.Image, kurunSchemaPrefix) {
							goFilesPath := strings.TrimPrefix(c.Image, kurunSchemaPrefix)
							pod.Spec.Containers[i].Image, err = buildImage([]string{goFilesPath})
							if err != nil {
								return err
							}

							pod.Spec.Containers[i].ImagePullPolicy = corev1.PullNever
						}
					}

					resource = pod

				case "Deployment":
					deployment, err := unstructuredToDeployment(obj)
					if err != nil {
						return err
					}

					for i, c := range deployment.Spec.Template.Spec.Containers {
						if strings.HasPrefix(c.Image, kurunSchemaPrefix) {
							goFilesPath := strings.TrimPrefix(c.Image, kurunSchemaPrefix)
							deployment.Spec.Template.Spec.Containers[i].Image, err = buildImage([]string{goFilesPath})
							if err != nil {
								return err
							}

							deployment.Spec.Template.Spec.Containers[i].ImagePullPolicy = corev1.PullNever
						}
					}

					resource = deployment
				default:
					resource = obj
				}

				rawResource, err := yaml.Marshal(resource)
				if err != nil {
					return err
				}

				rawResources = append(rawResources, rawResource)

				obj = nil
			}
		}

		resourceBuffer := bytes.NewBuffer(nil)

		for _, rawResource := range rawResources {
			_, err := resourceBuffer.Write(rawResource)
			if err != nil {
				return err
			}

			_, err = resourceBuffer.WriteString("\n---\n")
			if err != nil {
				return err
			}
		}

		kubectlArgs := append([]string{"apply", "-f", "-"}, args...)

		kubectlCommand := exec.Command("kubectl", kubectlArgs...)
		kubectlCommand.Stdin = resourceBuffer
		kubectlCommand.Stderr = os.Stderr
		kubectlCommand.Stdout = os.Stdout

		if err := kubectlCommand.Run(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}

		return nil
	},
}

func unstructuredToPod(obj *unstructured.Unstructured) (*v1.Pod, error) {
	json, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		return nil, err
	}
	pod := new(v1.Pod)
	err = runtime.DecodeInto(clientscheme.Codecs.LegacyCodec(v1.SchemeGroupVersion), json, pod)
	return pod, err
}

func unstructuredToDeployment(obj *unstructured.Unstructured) (*appsv1.Deployment, error) {
	json, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		return nil, err
	}
	deployment := new(appsv1.Deployment)
	err = runtime.DecodeInto(clientscheme.Codecs.LegacyCodec(v1.SchemeGroupVersion), json, deployment)
	return deployment, err
}

func main() {
	runCmd.PersistentFlags().StringVar(&serviceAccount, "serviceaccount", "", "Service account to set for the pod")
	runCmd.PersistentFlags().StringArrayVarP(&podEnv, "env", "e", []string{}, "Environment variables to pass to the pod's containers")

	portForwardCmd.PersistentFlags().IntVar(&servicePort, "serviceport", 80, "Service port to set for the service")
	portForwardCmd.PersistentFlags().StringVar(&serviceName, "servicename", "kurun", "Service name to set for the service")
	portForwardCmd.PersistentFlags().StringSliceVarP(&labels, "label", "l", []string{}, "Pod labels to add")
	portForwardCmd.PersistentFlags().StringVar(&tlsSecret, "tlssecret", "", "Use the certs for ghostunnel")
	portForwardCmd.PersistentFlags().IntVar(&localPort, "localport", 8444, "Local port to use for port-forwarding")

	applyCmd.PersistentFlags().StringSliceVarP(&files, "filename", "f", []string{}, "Filename, or URL to files to use to create the resource (use - for STDIN)")

	rootCmd := &cobra.Command{Use: "kurun"}
	rootCmd.PersistentFlags().StringVar(&namespace, "namespace", "default", "Namespace to use for the Pod/Service")
	rootCmd.AddCommand(runCmd, portForwardCmd, applyCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(2)
	}
}
