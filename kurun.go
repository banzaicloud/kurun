package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var serviceAccount string
var servicePort int
var serviceName string
var tlsSecret string
var podEnv []string
var namespace string
var labels []string

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
	if namespace != "" {
		args = append(args, fmt.Sprintf("--namespace=%s", namespace))
	}

	cmd := exec.Command("kubectl", args...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	return cmd.Run()
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

		os.MkdirAll("/tmp/kurun", os.ModePerm)

		goBuildArgs := []string{"build", "-o", "/tmp/kurun/main"}
		for _, gofile := range gofiles {
			goBuildArgs = append(goBuildArgs, gofile)
		}
		goBuildCommand := exec.Command("go", goBuildArgs...)
		goBuildCommand.Stderr = os.Stderr
		goBuildCommand.Stdout = os.Stdout
		env := os.Environ()
		env = append(env, "GOOS=linux", "CGO_ENABLED=0")
		goBuildCommand.Env = env

		if err := goBuildCommand.Start(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}
		if err := goBuildCommand.Wait(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}

		file, err := os.Create("/tmp/kurun/Dockerfile")
		if err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}
		defer file.Close()

		fmt.Fprintf(file, "FROM alpine\nADD main /\n")

		dockerBuildCommand := exec.Command("docker", "build", "-t", "kurun", "/tmp/kurun")
		dockerBuildCommand.Stderr = os.Stderr
		dockerBuildCommand.Stdout = os.Stdout

		if err := dockerBuildCommand.Start(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}
		if err := dockerBuildCommand.Wait(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}

		kubectlArgs := []string{
			"run", "kurun",
			"-it",
			"--image=kurun",
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

		if namespace != "" {
			kubectlArgs = append(kubectlArgs, fmt.Sprintf("--namespace=%s", namespace))
		}

		kubectlArgs = append(kubectlArgs, "--command", "--", "sh", "-c", fmt.Sprintf("sleep 1 && /main %s", strings.Join(finalArguments[:], " ")))
		kubectlCommand := exec.Command("kubectl", kubectlArgs...)
		kubectlCommand.Stdin = os.Stdin
		kubectlCommand.Stderr = os.Stderr
		kubectlCommand.Stdout = os.Stdout

		if err := kubectlCommand.Start(); err != nil {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}
		if err := kubectlCommand.Wait(); err != nil {
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
				ContainerPort: 8000,
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
				Image:   "alexellis2/inlets:2.4.1",
				Command: []string{"inlets", "server", "-p", fmt.Sprint(inletsPort)},
				Ports:   ports,
			},
		}

		volumes := []corev1.Volume{}

		if tlsSecret != "" {

			containers = append(containers, corev1.Container{
				Name:  "ghostunnel",
				Image: "squareup/ghostunnel:v1.5.0-rc.2",
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
				"--target-port=8000",
			}

			if err := runKubectl(kubectlArgs); err != nil {
				return err
			}
		}

		////////

		kubectlArgs = []string{
			"port-forward",
			"deployment/" + deploymentName,
			"8000:" + fmt.Sprint(inletsPort),
		}

		if namespace != "" {
			kubectlArgs = append(kubectlArgs, fmt.Sprintf("--namespace=%s", namespace))
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

		fmt.Println("Waiting for kubeclt port-forward to build up the connection")
		time.Sleep(2 * time.Second)

		/////

		upstream := args[0]

		inletsCommand := exec.Command("inlets", "client", "--upstream", upstream)
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

func main() {
	runCmd.PersistentFlags().StringVar(&serviceAccount, "serviceaccount", "", "Service account to set for the pod")
	runCmd.PersistentFlags().StringArrayVarP(&podEnv, "env", "e", []string{}, "Environment variables to pass to the pod's containers")

	portForwardCmd.PersistentFlags().IntVar(&servicePort, "serviceport", 80, "Service port to set for the service")
	portForwardCmd.PersistentFlags().StringVar(&serviceName, "servicename", "kurun", "Service name to set for the service")
	portForwardCmd.PersistentFlags().StringSliceVarP(&labels, "label", "l", []string{}, "Pod labels to add")
	portForwardCmd.PersistentFlags().StringVar(&tlsSecret, "tlssecret", "", "Use the certs for ghostunnel")

	rootCmd := &cobra.Command{Use: "kurun"}
	rootCmd.PersistentFlags().StringVar(&namespace, "namespace", "", "Namespace to use for the Pod/Service")
	rootCmd.AddCommand(runCmd, portForwardCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(2)
	}
}
