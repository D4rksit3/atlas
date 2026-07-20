package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/atlasctl/atlas/pkg/api"
)

// KubeCollector lee el estado de un clúster REAL mediante la API de Kubernetes
// (client-go). Es el reemplazo de SampleCollector para producción.
//
// Alcance actual: nodos y cargas (Deployments/StatefulSets). Las conexiones
// reales entre servicios (Links) NO están en la API de K8s: vienen de Hubble
// (Cilium) y se integran en fase 2.
type KubeCollector struct {
	client  kubernetes.Interface
	timeout time.Duration
}

// NewKubeCollector construye el colector resolviendo la configuración de acceso:
//  1. in-cluster (cuando el agente corre como Pod con un ServiceAccount), o
//  2. un kubeconfig (desarrollo local contra k3s: KUBECONFIG o ~/.kube/config).
func NewKubeCollector(kubeconfig string) (*KubeCollector, error) {
	cfg, err := buildConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("configurando acceso al clúster: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creando cliente de Kubernetes: %w", err)
	}
	return &KubeCollector{client: cs, timeout: 10 * time.Second}, nil
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	// 1) Dentro del clúster.
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	// 2) Kubeconfig local.
	if kubeconfig == "" {
		if env := os.Getenv("KUBECONFIG"); env != "" {
			kubeconfig = env
		} else if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// Collect lee nodos y cargas del clúster y los mapea al modelo de Atlas.
func (c *KubeCollector) Collect() (api.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	var snap api.Snapshot

	nodes, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return snap, fmt.Errorf("listando nodos: %w", err)
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		snap.Nodes = append(snap.Nodes, api.Node{
			Name:  n.Name,
			Role:  nodeRole(n),
			Ready: nodeReady(n),
		})
	}

	deploys, err := c.client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return snap, fmt.Errorf("listando deployments: %w", err)
	}
	for i := range deploys.Items {
		d := &deploys.Items[i]
		snap.Workloads = append(snap.Workloads, api.Workload{
			Name:      d.Name,
			Namespace: d.Namespace,
			Kind:      "Deployment",
			Replicas:  int(d.Status.ReadyReplicas),
		})
	}

	sts, err := c.client.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return snap, fmt.Errorf("listando statefulsets: %w", err)
	}
	for i := range sts.Items {
		s := &sts.Items[i]
		snap.Workloads = append(snap.Workloads, api.Workload{
			Name:      s.Name,
			Namespace: s.Namespace,
			Kind:      "StatefulSet",
			Replicas:  int(s.Status.ReadyReplicas),
		})
	}

	// TODO(fase 2): poblar snap.Links desde Hubble (Cilium) — las conexiones
	// reales entre servicios no están en la API de Kubernetes.
	return snap, nil
}

// nodeRole distingue control-plane de worker por las labels estándar de K8s.
func nodeRole(n *corev1.Node) string {
	if _, ok := n.Labels["node-role.kubernetes.io/control-plane"]; ok {
		return "control-plane"
	}
	if _, ok := n.Labels["node-role.kubernetes.io/master"]; ok {
		return "control-plane"
	}
	return "worker"
}

// nodeReady devuelve true si la condición NodeReady está en True.
func nodeReady(n *corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
