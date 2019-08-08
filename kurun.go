package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var serviceAccount string
var servicePort int
var serviceName string
var podEnv []string
var namespace string
var labels []string

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

var exposeCmd = &cobra.Command{
	Use:     "port-forward [flags] upstream",
	Short:   "Just like `kubectl port-forward ...`, just the other way around!",
	Example: "kurun port-forward --namespace apps localhost:4443",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {

		done := make(chan bool, 1)

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

		go func() {
			<-signals
			fmt.Println("\rCtrl+C pressed, exiting")

			kubectlArgs := []string{
				"delete",
				"deployment",
				serviceName,
			}

			if err := runKubectl(kubectlArgs); err != nil {
				fmt.Println("failed to delete deployment", err.Error())
			}

			/////

			kubectlArgs = []string{
				"delete",
				"service",
				serviceName,
			}

			if err := runKubectl(kubectlArgs); err != nil {
				fmt.Println("failed to delete service", err.Error())
			}

			done <- true
		}()

		kubectlArgs := []string{
			"run", serviceName,
			"--image=alexellis2/inlets:2.20",
			"--limits=cpu=100m,memory=128Mi",
			"--port=8000",
			"--labels=" + strings.Join(labels, ","),
			"--command", "inlets", "server",
		}

		if err := runKubectl(kubectlArgs); err != nil {
			return err
		}

		//////

		kubectlArgs = []string{
			"wait",
			"--for=condition=available",
			"deployment/" + serviceName,
			"--timeout=60s",
		}

		if err := runKubectl(kubectlArgs); err != nil {
			return err
		}

		//////

		kubectlArgs = []string{
			"expose",
			"deployment",
			serviceName,
			"--port=" + fmt.Sprint(servicePort),
			"--target-port=8000",
		}

		if err := runKubectl(kubectlArgs); err != nil {
			return err
		}

		////////

		kubectlArgs = []string{
			"port-forward",
			"service/" + serviceName,
			"8000:" + fmt.Sprint(servicePort),
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

	exposeCmd.PersistentFlags().IntVar(&servicePort, "serviceport", 80, "Service port to set for the service")
	exposeCmd.PersistentFlags().StringVar(&serviceName, "servicename", "kurun", "Service name to set for the service")
	exposeCmd.PersistentFlags().StringSliceVarP(&labels, "label", "l", []string{}, "Pod labels to add")

	rootCmd := &cobra.Command{Use: "kurun"}
	rootCmd.PersistentFlags().StringVar(&namespace, "namespace", "", "Namespace to use for the Pod/Service")
	rootCmd.AddCommand(runCmd, exposeCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(2)
	}
}
