package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memcache "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	"github.com/atlasctl/atlas/pkg/api"
)

// Actuator ejecuta en el clúster las acciones que ordena la GUI (vía el control
// plane). Es el complemento de escritura del Collector (que solo lee).
type Actuator interface {
	Execute(ctx context.Context, a api.Action) api.ActionResult
}

// addonSpec describe un complemento VETADO que se puede instalar. El catálogo es
// cerrado a propósito: el agente nunca aplica manifiestos arbitrarios de la GUI,
// solo estos, con la versión fijada. Dos formas: manifiesto (url) o chart de Helm.
type addonSpec struct {
	namespace string
	url       string     // manifiesto único (server-side apply)
	helm      *helmChart // o un chart de Helm (SDK compilado en el agente)
}

// helmChart describe un chart de Helm vetado (repo + chart + versión fijada).
type helmChart struct {
	repo    string
	chart   string
	version string
	release string
	values  map[string]interface{}
}

// Catálogo de complementos. Fijados a una versión concreta (cadena de confianza).
// Se corresponde con api.Addons() (metadatos que ve la GUI). NUNCA se instala
// nada fuera de este catálogo.
var addons = map[string]addonSpec{
	"argocd": {
		namespace: "argocd",
		url:       "https://raw.githubusercontent.com/argoproj/argo-cd/v2.11.7/manifests/install.yaml",
	},
	"kyverno": {
		namespace: "kyverno",
		url:       "https://github.com/kyverno/kyverno/releases/download/v1.12.6/install.yaml",
	},
	"metallb": {
		namespace: "metallb-system",
		url:       "https://raw.githubusercontent.com/metallb/metallb/v0.14.8/config/manifests/metallb-native.yaml",
	},
	"metrics-server": {
		namespace: "kube-system",
		url:       "https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml",
	},
	"falco": {
		namespace: "falco",
		helm: &helmChart{
			repo: "https://falcosecurity.github.io/charts", chart: "falco",
			version: "4.9.0", release: "falco",
			// Driver moderno (eBPF) para que corra sin módulos de kernel.
			values: map[string]interface{}{
				"driver": map[string]interface{}{"kind": "modern_ebpf"},
				"tty":    true,
			},
		},
	},
}

// KubeActuator aplica acciones con client-go: escalar, reiniciar e instalar
// complementos vetados. Escalar/reiniciar necesitan update/patch; instalar
// necesita permisos amplios (crear CRDs, roles…) — ver deploy/agent-addons.yaml.
type KubeActuator struct {
	client  kubernetes.Interface
	dyn     dynamic.Interface
	mapper  *restmapper.DeferredDiscoveryRESTMapper
	cfg     *rest.Config
	http    *http.Client
	timeout time.Duration
}

// NewKubeActuator resuelve la config igual que el colector (in-cluster o kubeconfig)
// y prepara los clientes tipado y dinámico + el mapeador REST (para aplicar YAML).
func NewKubeActuator(kubeconfig string) (*KubeActuator, error) {
	cfg, err := buildConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("configurando acceso al clúster: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creando cliente de Kubernetes: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creando cliente dinámico: %w", err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creando cliente de discovery: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memcache.NewMemCacheClient(dc))
	return &KubeActuator{
		client:  cs,
		dyn:     dyn,
		mapper:  mapper,
		cfg:     cfg,
		http:    &http.Client{Timeout: 30 * time.Second},
		timeout: 15 * time.Second,
	}, nil
}

// Execute despacha según el tipo de acción y devuelve el resultado.
func (a *KubeActuator) Execute(ctx context.Context, act api.Action) api.ActionResult {
	// Instalar puede tardar (bajar el manifiesto y aplicar decenas de recursos).
	timeout := a.timeout
	if act.Kind == api.ActionInstall {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	switch act.Kind {
	case api.ActionScale:
		err = a.scale(ctx, act)
	case api.ActionRestart:
		err = a.restart(ctx, act)
	case api.ActionInstall:
		err = a.installAddon(ctx, act.Addon)
	case api.ActionAddApp:
		err = a.addApp(ctx, act.App)
	case api.ActionSync:
		err = a.syncApp(ctx, act.App)
	case api.ActionRollback:
		err = a.rollbackApp(ctx, act.App)
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

// installAddon instala un complemento del catálogo: crea su namespace, descarga
// el manifiesto fijado y lo aplica (server-side apply).
func (a *KubeActuator) installAddon(ctx context.Context, name string) error {
	spec, ok := addons[name]
	if !ok {
		keys := make([]string, 0, len(addons))
		for k := range addons {
			keys = append(keys, k)
		}
		return fmt.Errorf("complemento no soportado: %q (catálogo: %s)", name, strings.Join(keys, ", "))
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: spec.namespace}}
	if _, err := a.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creando namespace %s: %w", spec.namespace, err)
	}
	// Chart de Helm o manifiesto único.
	if spec.helm != nil {
		return a.installHelm(ctx, spec.namespace, spec.helm)
	}
	manifest, err := a.fetch(ctx, spec.url)
	if err != nil {
		return err
	}
	return a.applyManifest(ctx, manifest, spec.namespace)
}

// addApp registra un proyecto GitOps: crea una Application de ArgoCD con auto-sync
// (prune + self-heal), de modo que los cambios en el repo se apliquen solos.
func (a *KubeActuator) addApp(ctx context.Context, spec *api.AppSpec) error {
	if spec == nil || spec.Name == "" || spec.RepoURL == "" || spec.Namespace == "" {
		return fmt.Errorf("faltan datos del proyecto (name, repoURL, namespace)")
	}
	rev := spec.Revision
	if rev == "" {
		rev = "HEAD"
	}
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      spec.Name,
			"namespace": "argocd",
		},
		"spec": map[string]interface{}{
			"project": "default",
			"source": map[string]interface{}{
				"repoURL":        spec.RepoURL,
				"path":           spec.Path,
				"targetRevision": rev,
			},
			"destination": map[string]interface{}{
				"server":    "https://kubernetes.default.svc",
				"namespace": spec.Namespace,
			},
			"syncPolicy": map[string]interface{}{
				"automated":   map[string]interface{}{"prune": true, "selfHeal": true},
				"syncOptions": []interface{}{"CreateNamespace=true"},
			},
		},
	}}
	return a.applyOne(ctx, app, "argocd")
}

// syncApp fuerza una sincronización del proyecto (Application) a su revisión
// objetivo (HEAD): pone .operation.sync y ArgoCD la ejecuta.
func (a *KubeActuator) syncApp(ctx context.Context, spec *api.AppSpec) error {
	if spec == nil || spec.Name == "" {
		return fmt.Errorf("falta el nombre del proyecto")
	}
	apps := a.dyn.Resource(argoAppGVR).Namespace("argocd")
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		app, err := apps.Get(ctx, spec.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("leyendo el proyecto: %w", err)
		}
		if err := unstructured.SetNestedMap(app.Object,
			map[string]interface{}{"prune": true}, "operation", "sync"); err != nil {
			return err
		}
		_, err = apps.Update(ctx, app, metav1.UpdateOptions{})
		return err
	})
}

// rollbackApp revierte el proyecto a la revisión anterior de su historial. Pausa
// el auto-sync para que ArgoCD no vuelva a avanzar a HEAD inmediatamente.
func (a *KubeActuator) rollbackApp(ctx context.Context, spec *api.AppSpec) error {
	if spec == nil || spec.Name == "" {
		return fmt.Errorf("falta el nombre del proyecto")
	}
	apps := a.dyn.Resource(argoAppGVR).Namespace("argocd")
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		app, err := apps.Get(ctx, spec.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("leyendo el proyecto: %w", err)
		}
		history, _, _ := unstructured.NestedSlice(app.Object, "status", "history")
		if len(history) < 2 {
			return fmt.Errorf("no hay una versión anterior a la que revertir")
		}
		prev, ok := history[len(history)-2].(map[string]interface{})
		if !ok {
			return fmt.Errorf("historial ilegible")
		}
		prevRev, _, _ := unstructured.NestedString(prev, "revision")
		if prevRev == "" {
			return fmt.Errorf("la versión anterior no tiene revisión")
		}
		// Pausa auto-sync (si no, se re-sincronizaría a HEAD) y sincroniza a la
		// revisión anterior.
		unstructured.RemoveNestedField(app.Object, "spec", "syncPolicy", "automated")
		if err := unstructured.SetNestedMap(app.Object,
			map[string]interface{}{"revision": prevRev, "prune": true}, "operation", "sync"); err != nil {
			return err
		}
		_, err = apps.Update(ctx, app, metav1.UpdateOptions{})
		return err
	})
}

func (a *KubeActuator) fetch(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("descargando manifiesto: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("descargando manifiesto: HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 16<<20)) // hasta 16 MiB
}

// applyManifest aplica un manifiesto multi-documento con server-side apply.
func (a *KubeActuator) applyManifest(ctx context.Context, manifest []byte, defaultNS string) error {
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	applied := 0
	for {
		raw := map[string]interface{}{}
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("parseando manifiesto: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: raw}
		if obj.GetKind() == "" {
			continue
		}
		if err := a.applyOne(ctx, obj, defaultNS); err != nil {
			return err
		}
		applied++
	}
	if applied == 0 {
		return fmt.Errorf("el manifiesto no tenía recursos")
	}
	return nil
}

func (a *KubeActuator) applyOne(ctx context.Context, obj *unstructured.Unstructured, defaultNS string) error {
	gvk := obj.GroupVersionKind()
	mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// El caché puede quedar viejo tras crear CRDs: refresca y reintenta.
		a.mapper.Reset()
		mapping, err = a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("sin mapeo REST para %s: %w", gvk.Kind, err)
		}
	}
	var ri dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = defaultNS
			obj.SetNamespace(ns)
		}
		ri = a.dyn.Resource(mapping.Resource).Namespace(ns)
	} else {
		ri = a.dyn.Resource(mapping.Resource)
	}
	data, err := obj.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	_, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data,
		metav1.PatchOptions{FieldManager: "atlas-agent", Force: &force})
	if err != nil {
		return fmt.Errorf("aplicando %s/%s: %w", gvk.Kind, obj.GetName(), err)
	}
	return nil
}
