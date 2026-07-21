package agent

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/atlasctl/atlas/pkg/api"
)

// Actuator ejecuta en el clúster las acciones que ordena la GUI (vía el control
// plane). Es el complemento de escritura del Collector (que solo lee).
type Actuator interface {
	Execute(ctx context.Context, a api.Action) api.ActionResult
}

// KubeActuator aplica acciones con client-go: escalar y reiniciar cargas.
// Requiere permisos de escritura (update/patch) sobre deployments/statefulsets.
type KubeActuator struct {
	client  kubernetes.Interface
	timeout time.Duration
}

// NewKubeActuator resuelve la config igual que el colector (in-cluster o kubeconfig).
func NewKubeActuator(kubeconfig string) (*KubeActuator, error) {
	cfg, err := buildConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("configurando acceso al clúster: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creando cliente de Kubernetes: %w", err)
	}
	return &KubeActuator{client: cs, timeout: 15 * time.Second}, nil
}

// Execute despacha según el tipo de acción y devuelve el resultado.
func (a *KubeActuator) Execute(ctx context.Context, act api.Action) api.ActionResult {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	var err error
	switch act.Kind {
	case api.ActionScale:
		err = a.scale(ctx, act)
	case api.ActionRestart:
		err = a.restart(ctx, act)
	default:
		err = fmt.Errorf("acción no soportada: %q", act.Kind)
	}
	if err != nil {
		return api.ActionResult{ID: act.ID, OK: false, Error: err.Error()}
	}
	return api.ActionResult{ID: act.ID, OK: true}
}

func (a *KubeActuator) scale(ctx context.Context, act api.Action) error {
	r := int32(act.Replicas)
	switch act.WorkloadKind {
	case "Deployment":
		d, err := a.client.AppsV1().Deployments(act.Namespace).Get(ctx, act.Workload, metav1.GetOptions{})
		if err != nil {
			return err
		}
		d.Spec.Replicas = &r
		_, err = a.client.AppsV1().Deployments(act.Namespace).Update(ctx, d, metav1.UpdateOptions{})
		return err
	case "StatefulSet":
		s, err := a.client.AppsV1().StatefulSets(act.Namespace).Get(ctx, act.Workload, metav1.GetOptions{})
		if err != nil {
			return err
		}
		s.Spec.Replicas = &r
		_, err = a.client.AppsV1().StatefulSets(act.Namespace).Update(ctx, s, metav1.UpdateOptions{})
		return err
	default:
		return fmt.Errorf("tipo de carga no soportado: %q", act.WorkloadKind)
	}
}

// restart fuerza un rollout tocando una anotación del template del pod, igual que
// hace `kubectl rollout restart`.
func (a *KubeActuator) restart(ctx context.Context, act api.Action) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"atlas.dev/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339)))
	switch act.WorkloadKind {
	case "Deployment":
		_, err := a.client.AppsV1().Deployments(act.Namespace).Patch(
			ctx, act.Workload, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		return err
	case "StatefulSet":
		_, err := a.client.AppsV1().StatefulSets(act.Namespace).Patch(
			ctx, act.Workload, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		return err
	default:
		return fmt.Errorf("tipo de carga no soportado: %q", act.WorkloadKind)
	}
}
