package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

	// Ubicación de pods: en qué nodos corre cada carga y cuántos pods en cada uno.
	// Es best-effort: si falla (p. ej. sin permiso de pods), seguimos sin ella.
	if err := c.fillPlacement(ctx, snap.Workloads); err != nil {
		return snap, fmt.Errorf("listando pods: %w", err)
	}

	// TODO(fase 2): poblar snap.Links desde Hubble (Cilium) — las conexiones
	// reales entre servicios no están en la API de Kubernetes.
	return snap, nil
}

// fillPlacement lista los pods y rellena Workload.Placement con el reparto por
// nodo. Un pod se atribuye a su carga dueña vía ownerReferences (ReplicaSet ->
// Deployment) y al nodo donde está agendado (spec.nodeName).
func (c *KubeCollector) fillPlacement(ctx context.Context, workloads []api.Workload) error {
	pods, err := c.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// clave "namespace/carga" -> (nodo -> nº de pods).
	spread := make(map[string]map[string]int)
	for i := range pods.Items {
		p := &pods.Items[i]
		node := p.Spec.NodeName
		if node == "" {
			continue // aún no agendado
		}
		name := ownerWorkload(p)
		if name == "" {
			continue
		}
		key := p.Namespace + "/" + name
		if spread[key] == nil {
			spread[key] = make(map[string]int)
		}
		spread[key][node]++
	}

	for i := range workloads {
		w := &workloads[i]
		byNode := spread[w.Namespace+"/"+w.Name]
		if len(byNode) == 0 {
			continue
		}
		placement := make([]api.Placement, 0, len(byNode))
		for node, n := range byNode {
			placement = append(placement, api.Placement{Node: node, Pods: n})
		}
		// Orden estable (más pods primero, luego por nombre) para un mapa consistente.
		sort.Slice(placement, func(a, b int) bool {
			if placement[a].Pods != placement[b].Pods {
				return placement[a].Pods > placement[b].Pods
			}
			return placement[a].Node < placement[b].Node
		})
		w.Placement = placement
	}
	return nil
}

// ownerWorkload devuelve el nombre de la carga dueña de un pod. Para pods de un
// Deployment, el dueño directo es un ReplicaSet ("<deploy>-<hash>"): le quitamos
// el sufijo de hash. Para StatefulSet/DaemonSet/Job, el nombre del dueño ya es
// el de la carga. Devuelve "" si el pod no tiene controlador.
func ownerWorkload(p *corev1.Pod) string {
	for _, o := range p.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			if i := strings.LastIndex(o.Name, "-"); i > 0 {
				return o.Name[:i]
			}
		}
		return o.Name
	}
	return ""
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
