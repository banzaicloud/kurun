package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func NewRunCommand(rootParams *rootCommandParams) *cobra.Command {
	var serviceAccount string
	var podEnv []string

	cmd := &cobra.Command{
		Use:   "run [flags] -- gofiles... [arguments...]",
		Short: "Just like `go run main.go` but executed inside Kubernetes with one command.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace := rootParams.namespace

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

			podName := strings.Split(image, ":")[0]

			podOverride := map[string]interface{}{
				"spec": corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  podName,
							Image: "docker.io/library/" + image,
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									"cpu":    resource.MustParse("100m"),
									"memory": resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			}

			overrides, err := json.Marshal(podOverride)
			if err != nil {
				return err
			}

			kubectlArgs := []string{
				"run", podName,
				mode,
				"--image=docker.io/library/" + image,
				"--quiet",
				"--image-pull-policy=IfNotPresent",
				"--restart=Never",
				"--rm",
				"--overrides=" + string(overrides),
			}

			if serviceAccount != "" {
				kubectlArgs = append(kubectlArgs, fmt.Sprintf("--serviceaccount=%s", serviceAccount))
			}

			for _, e := range podEnv {
				kubectlArgs = append(kubectlArgs, fmt.Sprintf("--env=%s", e))
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
	cmd.PersistentFlags().StringVar(&serviceAccount, "serviceaccount", "", "Service account to set for the pod")
	cmd.PersistentFlags().StringArrayVarP(&podEnv, "env", "e", []string{}, "Environment variables to pass to the pod's containers")

	return cmd
}
