package cmd

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sYaml "k8s.io/apimachinery/pkg/util/yaml"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
)

const kurunSchemaPrefix = "kurun://"

func NewApplyCommand() *cobra.Command {
	var files []string

	cmd := &cobra.Command{
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

					var resource map[string]interface{}

					switch obj.GetKind() {
					case "Pod":
						pod := new(corev1.Pod)
						if err := unstructuredToStructured(obj, pod); err != nil {
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

						resource, err = runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
						if err != nil {
							return err
						}

					case "Deployment":
						deployment := new(appsv1.Deployment)
						if err := unstructuredToStructured(obj, deployment); err != nil {
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

						resource, err = runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
						if err != nil {
							return err
						}

					default:
						resource, err = runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
						if err != nil {
							return err
						}
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

	cmd.PersistentFlags().StringSliceVarP(&files, "filename", "f", []string{}, "Filename or URL to files to use to create the resource (use - for STDIN)")

	return cmd
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

	err := os.MkdirAll(directory, os.ModePerm)
	if err != nil {
		return "", err
	}

	goBuildArgs := []string{"build", "-o", directory + "/main"}
	goBuildArgs = append(goBuildArgs, goFiles...)
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

func unstructuredToStructured(src *unstructured.Unstructured, dst runtime.Object) error {
	json, err := runtime.Encode(unstructured.UnstructuredJSONScheme, src)
	if err != nil {
		return err
	}
	return runtime.DecodeInto(clientscheme.Codecs.LegacyCodec(corev1.SchemeGroupVersion), json, dst)
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
