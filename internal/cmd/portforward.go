package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"emperror.dev/errors"
	"github.com/banzaicloud/kurun/tunnel"
	tunnelws "github.com/banzaicloud/kurun/tunnel/websocket"
	"github.com/go-logr/stdr"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

const kurunServerImage = "ghcr.io/banzaicloud/kurun-server:v0.2.1"

func NewPortForwardCommand(rootParams *rootCommandParams) *cobra.Command {
	var (
		labels      []string
		serverImage string
		serviceName string
		servicePort int
		tlsSecret   string
	)

	cmd := &cobra.Command{
		Use:     "port-forward [flags] upstream",
		Short:   "Just like `kubectl port-forward ...`, just the other way around!",
		Example: "kurun port-forward --namespace apps localhost:4443",
		Args:    cobra.MinimumNArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			namespace := rootParams.namespace
			verbosity := rootParams.verbosity

			controlPort := corev1.ContainerPort{
				Name:          "control",
				ContainerPort: 8333,
			}
			requestPort := corev1.ContainerPort{
				Name:          "request",
				ContainerPort: 8444,
			}

			downstream := args[0]
			downstreamURL := &url.URL{
				Host: downstream,
			}
			if strings.Contains(downstream, ":/") {
				var err error
				downstreamURL, err = url.Parse(downstream)
				if err != nil {
					return errors.WithMessage(err, "failed to parse downstream URL")
				}
			}
			if downstreamURL.Scheme == "" {
				downstreamURL.Scheme = "http"
				if tlsSecret != "" {
					downstreamURL.Scheme = "https"
				}
			}

			stdr.SetVerbosity(verbosity)
			logger := stdr.New(log.New(os.Stdout, "", log.LstdFlags|log.LUTC))

			cmdCtx, cancelCmdCtx := context.WithCancel(cmd.Context())
			defer cancelCmdCtx()

			go func() {
				signals := make(chan os.Signal, 1)
				signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

				select {
				case <-signals:
					cancelCmdCtx()
					fmt.Fprintln(os.Stdout, "Ctrl+C pressed, exiting...")
				case <-cmdCtx.Done():
				}
			}()

			deploymentName := serviceName
			if !strings.HasSuffix(deploymentName, "kurun") {
				deploymentName += "-kurun"
			}

			labelsMap := map[string]string{
				"app.kubernetes.io/name": deploymentName,
			}

			for _, label := range labels {
				labelPair := strings.Split(label, "=")
				if len(labelPair) == 2 {
					labelsMap[labelPair[0]] = labelPair[1]
				}
			}

			cmd.SilenceUsage = true // all args and flags validated before this line

			kubeConfig, err := config.GetConfig()
			if err != nil {
				return err
			}

			kubeCluster, err := cluster.New(kubeConfig, func(o *cluster.Options) {
				o.Namespace = namespace
			})
			if err != nil {
				return err
			}

			go kubeCluster.Start(cmdCtx)

			if !kubeCluster.GetCache().WaitForCacheSync(cmdCtx) {
				return errors.New("cache did not sync")
			}

			kubeClient := kubeCluster.GetClient()

			kurunServiceCreated := false
			kurunService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      serviceName,
					Labels:    labelsMap,
				},
				Spec: corev1.ServiceSpec{
					Selector: labelsMap,
					Ports: []corev1.ServicePort{
						{
							Name:       "request",
							Port:       int32(servicePort),
							TargetPort: intstr.FromString(requestPort.Name),
						},
						{
							Name:       "control",
							Port:       controlPort.ContainerPort,
							TargetPort: intstr.FromString(controlPort.Name),
						},
					},
				},
			}
			if err := kubeClient.Get(cmdCtx, client.ObjectKeyFromObject(kurunService), kurunService); err != nil {
				if apierrors.IsNotFound(err) {
					if err := kubeClient.Create(cmdCtx, kurunService); err != nil {
						return err
					}
					kurunServiceCreated = true
				} else {
					return err
				}
			}

			defer func() {
				if kurunServiceCreated {
					if err := kubeClient.Delete(context.TODO(), kurunService); err != nil {
						logger.Error(err, "failed to delete service")
					}
				}
			}()

			if !kurunServiceCreated {
				labelsMap = kurunService.Spec.Selector

				for _, port := range kurunService.Spec.Ports {
					switch port.Name {
					case "request":
						setContainerPortFromServicePort(&requestPort, &port)
					case "control":
						setContainerPortFromServicePort(&controlPort, &port)
					}
				}
			}

			tunnelServerContainer := corev1.Container{
				Name:            "tunnel-server",
				Image:           serverImage,
				ImagePullPolicy: corev1.PullIfNotPresent, // HACK
				Args: []string{
					"--ctrl-srv-addr",
					fmt.Sprintf("0.0.0.0:%d", controlPort.ContainerPort),
					"--ctrl-srv-self-signed",
					"--req-srv-addr",
					fmt.Sprintf("0.0.0.0:%d", requestPort.ContainerPort),
				},
				Ports: []corev1.ContainerPort{
					requestPort,
					controlPort,
				},
			}

			volumes := []corev1.Volume{}

			requestScheme := "http"
			if tlsSecret != "" {
				requestScheme = "https"

				tunnelServerContainer.Args = append(
					tunnelServerContainer.Args,
					"--req-srv-cert",
					"/etc/tls/tls.crt",
					"--req-srv-key",
					"/etc/tls/tls.key",
					"-v",
				)
				tunnelServerContainer.VolumeMounts = []corev1.VolumeMount{
					{
						Name:      tlsSecret,
						MountPath: "/etc/tls",
					},
				}
				volumes = append(volumes, corev1.Volume{
					Name: tlsSecret,
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: tlsSecret,
						},
					},
				})
			}

			deployment := &appsv1.Deployment{
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
							Containers: []corev1.Container{
								tunnelServerContainer,
							},
							Volumes: volumes,
						},
					},
				},
			}

			if err := kubeClient.Create(cmdCtx, deployment); err != nil {
				return err
			}

			defer func() {
				if err := kubeClient.Delete(context.Background(), deployment); err != nil {
					logger.Error(err, "failed to delete deployment")
				}
			}()

			if err := waitForResource(cmdCtx, kubeCluster.GetCache(), kubeCluster.GetScheme(), deployment, func(obj interface{}) bool {
				deploy, ok := obj.(*appsv1.Deployment)
				return ok && deploy.Namespace == deployment.Namespace && deploy.Name == deployment.Name && hasAvailable(deploy)
			}, 60*time.Second); err != nil {
				return err
			}

			proxyURL, err := url.Parse(kubeConfig.Host)
			if err != nil {
				return err
			}
			if proxyURL.Scheme != "https" {
				panic("API server URL not HTTPS")
			}
			proxyURL.Scheme = "wss"
			proxyURL.Path = fmt.Sprintf("/api/v1/namespaces/%s/services/https:%s:%d/proxy/", namespace, kurunService.Name, selectServicePort(kurunService, "control").Port)

			proxyTLSCfg, err := rest.TLSConfigFor(kubeConfig)
			if err != nil {
				return err
			}
			proxyTLSCfg.InsecureSkipVerify = true

			baseTransport := &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // TODO: add flags for insecure and ca cert
				},
			}
			transport := tunnel.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme = downstreamURL.Scheme
				r.URL.Host = downstreamURL.Host
				if downstreamURL.Path != "" {
					r.URL.Path = path.Join(downstreamURL.Path, r.URL.Path)
				}
				return baseTransport.RoundTrip(r)
			})

			tunnelClientCfg := tunnelws.NewClientConfig(
				proxyURL.String(),
				transport,
				tunnelws.WithLogger(logger),
				tunnelws.WithDialerCtor(func() *websocket.Dialer {
					return &websocket.Dialer{
						TLSClientConfig: proxyTLSCfg.Clone(),
					}
				}),
			)
			go func() {
				if err := tunnelws.RunClient(cmdCtx, *tunnelClientCfg); err != nil {
					logger.Error(err, "tunnel client exited with error")
				}
				cancelCmdCtx()
			}()

			fmt.Fprintf(os.Stdout, "Forwarding %s://%s.%s.svc:%d -> %s\n",
				requestScheme, kurunService.Name, kurunService.Namespace, selectServicePort(kurunService, "request").Port,
				downstreamURL.String())

			<-cmdCtx.Done()

			return nil
		},
	}

	cmd.PersistentFlags().StringSliceVarP(&labels, "label", "l", []string{}, "Pod labels to add")
	cmd.PersistentFlags().StringVar(&serverImage, "server-image", kurunServerImage, "kurun tunnel server image to use")
	cmd.PersistentFlags().StringVar(&serviceName, "servicename", "kurun", "Service name to set for the service")
	cmd.PersistentFlags().IntVar(&servicePort, "serviceport", 80, "Service port to set for the service")
	cmd.PersistentFlags().StringVar(&tlsSecret, "tlssecret", "", "Use the certs for kurun-server")

	return cmd
}

func waitForResource(ctx context.Context, kubeCache cache.Cache, scheme *runtime.Scheme, obj client.Object, filter func(interface{}) bool, timeout time.Duration) error {
	done := make(chan struct{}, 1)
	informer, err := kubeCache.GetInformer(ctx, obj)
	if err != nil {
		return err
	}
	informer.AddEventHandler(toolscache.FilteringResourceEventHandler{
		FilterFunc: filter,
		Handler: toolscache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				select {
				case done <- struct{}{}:
				default:
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				select {
				case done <- struct{}{}:
				default:
				}
			},
		},
	})

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		resourceType := "resource"
		if gvk, err := apiutil.GVKForObject(obj, scheme); err == nil {
			resourceType = strings.ToLower(gvk.Kind)
		}
		return errors.Errorf("timeout waiting for %s", resourceType)
	}
}

func hasAvailable(deployment *appsv1.Deployment) bool {
	if deployment == nil {
		return false
	}
	for _, cond := range deployment.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func setContainerPortFromServicePort(cp *corev1.ContainerPort, sp *corev1.ServicePort) {
	if portName := sp.TargetPort.StrVal; portName != "" {
		cp.Name = portName
	} else if portNum := sp.TargetPort.IntVal; portNum != 0 {
		cp.ContainerPort = portNum
	} else {
		cp.ContainerPort = sp.Port
	}
}

func selectServicePort(svc *corev1.Service, name string) *corev1.ServicePort {
	if svc != nil {
		for i := range svc.Spec.Ports {
			port := &svc.Spec.Ports[i]
			if port.Name == name {
				return port
			}
		}
	}
	return nil
}
