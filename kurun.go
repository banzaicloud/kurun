package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var serviceAccount string
var podEnv []string
var namespace string

var rootCmd = &cobra.Command{
	Use:   "kurun [flags] -- gofiles... [arguments...]",
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

func main() {
	rootCmd.PersistentFlags().StringVar(&serviceAccount, "serviceaccount", "", "Service account to set for the pod")
	rootCmd.PersistentFlags().StringArrayVar(&podEnv, "env", []string{}, "Environment variables to pass to the pod's containers")
	rootCmd.PersistentFlags().StringVar(&namespace, "namespace", "", "Namespace to use for the pod")
	if err := rootCmd.Execute(); err != nil {
		os.Exit(2)
	}
}
