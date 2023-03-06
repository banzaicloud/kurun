package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func NewRunCommand(rootParams *rootCommandParams) *cobra.Command {
	var serviceAccount string
	var overrides string
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

			kubectlArgs := []string{
				"run", podName,
				mode,
				"--image=docker.io/library/" + image,
				"--quiet",
				"--image-pull-policy=IfNotPresent",
				"--restart=Never",
				"--rm",
				"--override-type=strategic",
			}

			limitsPatch := map[string]interface{}{
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

			limitsOverride, err := json.Marshal(limitsPatch)
			if err != nil {
				return err
			}

			combinedOverride := limitsOverride

			if serviceAccount != "" {
				serviceAccountPatch := fmt.Sprintf(`{"spec":{"serviceAccount":"%s"}}`, serviceAccount)
				serviceAccountOverride, err := jsonpatch.MergeMergePatches(combinedOverride, []byte(serviceAccountPatch))
				if err != nil {
					return err
				}
				combinedOverride = serviceAccountOverride
			}

			if overrides != "" {
				overridesOverride, err := jsonpatch.MergeMergePatches(combinedOverride, []byte(overrides))
				if err != nil {
					return err
				}
				combinedOverride = overridesOverride
			}

			kubectlArgs = append(kubectlArgs, fmt.Sprintf("--overrides=%s", string(combinedOverride)))

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
	cmd.PersistentFlags().StringVar(&overrides, "overrides", "", "An inline JSON override for the generated pod object, e.g. '{\"metadata\":{\"name\":\"my-pod\"}}'")
	cmd.PersistentFlags().StringArrayVarP(&podEnv, "env", "e", nil, "Environment variables to pass to the pod's containers")

	return cmd
}
