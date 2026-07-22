package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
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
	"ingress-nginx": {
		namespace: "ingress-nginx",
		helm: &helmChart{
			repo: "https://kubernetes.github.io/ingress-nginx", chart: "ingress-nginx",
			version: "4.11.3", release: "ingress-nginx",
		},
	},
	"cert-manager": {
		namespace: "cert-manager",
		helm: &helmChart{
			repo: "https://charts.jetstack.io", chart: "cert-manager",
			version: "v1.15.3", release: "cert-manager",
			// El chart no instala sus CRDs por defecto; sin ellas cert-manager no
			// arranca. crds.enabled las incluye en el release (modo recomendado v1.15+).
			values: map[string]interface{}{
				"crds": map[string]interface{}{"enabled": true},
			},
		},
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
	"kube-prometheus-stack": {
		namespace: "monitoring",
		helm: &helmChart{
			repo:    "https://prometheus-community.github.io/helm-charts",
			chart:   "kube-prometheus-stack",
			version: "62.7.0", release: "kube-prometheus-stack",
			values: map[string]interface{}{
				// Permite EMBEBER Grafana dentro de la GUI de Atlas (vista
				// "Administrar"): sin esto, Grafana manda X-Frame-Options: deny
				// y el navegador bloquea el iframe.
				"grafana": map[string]interface{}{
					"grafana.ini": map[string]interface{}{
						"security": map[string]interface{}{"allow_embedding": true},
					},
				},
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
	// Instalar puede tardar (bajar el chart/manifiesto y aplicar muchos recursos;
	// kube-prometheus-stack en particular es grande).
	timeout := a.timeout
	if act.Kind == api.ActionInstall || act.Kind == api.ActionUninstall {
		timeout = 6 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	var output string
	switch act.Kind {
	case api.ActionLogs:
		output, err = a.workloadLogs(ctx, act.Namespace, act.Workload)
	case api.ActionEvents:
		output, err = a.namespaceEvents(ctx, act.Namespace)
	case api.ActionScale:
		err = a.scale(ctx, act)
	case api.ActionRestart:
		err = a.restart(ctx, act)
	case api.ActionInstall:
		err = a.installAddon(ctx, act.Addon, act.Values)
	case api.ActionAddApp:
		err = a.addApp(ctx, act.App)
	case api.ActionIssuer:
		err = a.createIssuer(ctx, act.Issuer)
	case api.ActionExpose:
		err = a.expose(ctx, act.Expose)
	case api.ActionUninstall:
		err = a.uninstallAddon(ctx, act.Addon)
	case api.ActionUnexpose:
		err = a.unexpose(ctx, act.Expose)
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
	return api.ActionResult{ID: act.ID, OK: true, Output: output}
}

// workloadLogs devuelve la cola de logs de los pods de una carga (hasta 2 pods,
// 120 líneas por pod), con una cabecera por pod. Salida acotada.
func (a *KubeActuator) workloadLogs(ctx context.Context, namespace, workload string) (string, error) {
	pods, err := a.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listando pods: %w", err)
	}
	var mine []corev1.Pod
	for i := range pods.Items {
		if ownerWorkload(&pods.Items[i]) == workload {
			mine = append(mine, pods.Items[i])
		}
	}
	if len(mine) == 0 {
		return "", fmt.Errorf("la carga %s/%s no tiene pods", namespace, workload)
	}
	if len(mine) > 2 {
		mine = mine[:2]
	}
	tail := int64(120)
	var b strings.Builder
	for i := range mine {
		p := &mine[i]
		fmt.Fprintf(&b, "── pod %s (%s) ──\n", p.Name, p.Status.Phase)
		raw, err := a.client.CoreV1().Pods(namespace).
			GetLogs(p.Name, &corev1.PodLogOptions{TailLines: &tail}).Do(ctx).Raw()
		if err != nil {
			fmt.Fprintf(&b, "(sin logs: %v)\n", err)
			continue
		}
		b.Write(raw)
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	out := b.String()
	if len(out) > api.MaxActionOutput {
		out = "…(recortado)…\n" + out[len(out)-api.MaxActionOutput:]
	}
	return out, nil
}

// namespaceEvents devuelve los eventos recientes de un namespace (más nuevos
// primero, hasta 40), en formato legible.
func (a *KubeActuator) namespaceEvents(ctx context.Context, namespace string) (string, error) {
	evs, err := a.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listando eventos: %w", err)
	}
	items := evs.Items
	sort.Slice(items, func(i, j int) bool {
		return eventTime(&items[i]).After(eventTime(&items[j]))
	})
	if len(items) > 40 {
		items = items[:40]
	}
	if len(items) == 0 {
		return "(sin eventos recientes en " + namespace + ")", nil
	}
	var b strings.Builder
	for i := range items {
		e := &items[i]
		fmt.Fprintf(&b, "%s  %-7s %-20s %s/%s: %s\n",
			eventTime(e).Format("15:04:05"), e.Type, e.Reason,
			e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Message)
	}
	out := b.String()
	if len(out) > api.MaxActionOutput {
		out = out[:api.MaxActionOutput] + "\n…(recortado)…"
	}
	return out, nil
}

// eventTime devuelve el instante más representativo de un evento.
func eventTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
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

// installAddon instala un complemento del catálogo: crea su namespace y aplica el
// manifiesto fijado (server-side apply) o el chart de Helm. userValues son valores
// editables que solo se aplican en los paths VETADOS del catálogo.
func (a *KubeActuator) installAddon(ctx context.Context, name string, userValues map[string]string) error {
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
		values := mergeAddonValues(name, spec.helm.values, userValues)
		return a.installHelm(ctx, spec.namespace, spec.helm, values)
	}
	manifest, err := a.fetch(ctx, spec.url)
	if err != nil {
		return err
	}
	return a.applyManifest(ctx, manifest, spec.namespace)
}

// mergeAddonValues parte de los values base del chart y superpone los valores del
// usuario SOLO en los paths declarados en el catálogo (api.AddonParams). Ignora
// cualquier clave que no sea un parámetro vetado del complemento.
func mergeAddonValues(addon string, base map[string]interface{}, user map[string]string) map[string]interface{} {
	// Copia PROFUNDA: los values base viven en el catálogo compartido del
	// paquete; setNestedValue no debe mutarlos (p. ej. escribir la contraseña
	// del usuario dentro del mapa base "grafana" de todas las instalaciones).
	out := deepCopyValues(base)
	for _, p := range api.AddonParams(addon) {
		raw, ok := user[p.Key]
		if !ok || raw == "" {
			continue
		}
		setNestedValue(out, strings.Split(p.Path, "."), typedValue(p.Type, raw))
	}
	return out
}

// setNestedValue fija value en out siguiendo la ruta (creando mapas intermedios).
// deepCopyValues clona un árbol de values (mapas anidados; las hojas se copian
// por valor de referencia, no se mutan).
func deepCopyValues(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if mm, ok := v.(map[string]interface{}); ok {
			out[k] = deepCopyValues(mm)
		} else {
			out[k] = v
		}
	}
	return out
}

func setNestedValue(out map[string]interface{}, path []string, value interface{}) {
	for i := 0; i < len(path)-1; i++ {
		next, ok := out[path[i]].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			out[path[i]] = next
		}
		out = next
	}
	out[path[len(path)-1]] = value
}

func typedValue(kind, raw string) interface{} {
	switch kind {
	case "int":
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	case "bool":
		return raw == "true" || raw == "1"
	}
	return raw
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

// createIssuer crea un ClusterIssuer de cert-manager (emisor ACME/Let's Encrypt)
// con reto HTTP-01 resuelto por el Ingress. El servidor ACME se DERIVA del entorno
// vetado (staging/production) — la GUI nunca manda una URL arbitraria. A partir de
// aquí, publicar un servicio con TLS es anotar el Ingress con este emisor.
func (a *KubeActuator) createIssuer(ctx context.Context, spec *api.IssuerSpec) error {
	if spec == nil {
		return fmt.Errorf("falta la definición del emisor")
	}
	if !strings.Contains(spec.Email, "@") {
		return fmt.Errorf("email ACME inválido")
	}
	server, ok := api.ACMEDirectory(spec.Environment)
	if !ok {
		return fmt.Errorf("entorno ACME no soportado: %q", spec.Environment)
	}
	name := spec.IssuerName()
	issuer := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"acme": map[string]interface{}{
				"email":  spec.Email,
				"server": server,
				// cert-manager guarda aquí la clave de la cuenta ACME (la crea él).
				"privateKeySecretRef": map[string]interface{}{"name": name},
				"solvers": []interface{}{
					map[string]interface{}{
						"http01": map[string]interface{}{
							"ingress": map[string]interface{}{"ingressClassName": spec.IngressClassOr()},
						},
					},
				},
			},
		},
	}}
	// ClusterIssuer es cluster-scoped; applyOne lo detecta por el RESTMapping.
	return a.applyOne(ctx, issuer, "")
}

// expose publica un servicio: crea (o actualiza) el Ingress "atlas-<service>"
// que enruta el host al Service indicado. Con TLS, lo anota con el ClusterIssuer
// para que cert-manager emita el certificado del host automáticamente.
func (a *KubeActuator) expose(ctx context.Context, spec *api.ExposeSpec) error {
	if spec == nil {
		return fmt.Errorf("falta la definición del servicio a publicar")
	}
	// El Service debe existir: mejor un error claro ahora que un 503 después.
	if _, err := a.client.CoreV1().Services(spec.Namespace).Get(ctx, spec.Service, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("el servicio %s/%s no existe: %w", spec.Namespace, spec.Service, err)
	}
	name := "atlas-" + spec.Service
	meta := map[string]interface{}{
		"name":      name,
		"namespace": spec.Namespace,
		"labels":    map[string]interface{}{"app.kubernetes.io/managed-by": "atlas"},
	}
	ingSpec := map[string]interface{}{
		"ingressClassName": spec.IngressClassOr(),
		"rules": []interface{}{
			map[string]interface{}{
				"host": spec.Host,
				"http": map[string]interface{}{
					"paths": []interface{}{
						map[string]interface{}{
							"path":     "/",
							"pathType": "Prefix",
							"backend": map[string]interface{}{
								"service": map[string]interface{}{
									"name": spec.Service,
									"port": map[string]interface{}{"number": int64(spec.Port)},
								},
							},
						},
					},
				},
			},
		},
	}
	if spec.TLS {
		meta["annotations"] = map[string]interface{}{
			"cert-manager.io/cluster-issuer": spec.IssuerOr(),
		}
		ingSpec["tls"] = []interface{}{
			map[string]interface{}{
				"hosts":      []interface{}{spec.Host},
				"secretName": name + "-tls",
			},
		}
	}
	ing := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata":   meta,
		"spec":       ingSpec,
	}}
	return a.applyOne(ctx, ing, spec.Namespace)
}

// sharedNamespaces son namespaces que NUNCA se borran al desinstalar: no son
// propiedad de ningún complemento.
var sharedNamespaces = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"default": true, "atlas-system": true,
}

// uninstallAddon quita un complemento del catálogo: helm uninstall si es un
// chart, o borrado de sus recursos (en orden inverso) si es un manifiesto. El
// namespace propio del complemento se elimina; los compartidos (kube-system…)
// jamás se tocan.
func (a *KubeActuator) uninstallAddon(ctx context.Context, name string) error {
	spec, ok := addons[name]
	if !ok {
		return fmt.Errorf("complemento no soportado: %q", name)
	}
	if spec.helm != nil {
		if err := a.uninstallHelm(spec.namespace, spec.helm); err != nil {
			return err
		}
	} else {
		manifest, err := a.fetch(ctx, spec.url)
		if err != nil {
			return err
		}
		if err := a.deleteManifest(ctx, manifest, spec.namespace); err != nil {
			return err
		}
	}
	if !sharedNamespaces[spec.namespace] {
		err := a.client.CoreV1().Namespaces().Delete(ctx, spec.namespace, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("borrando el namespace %s: %w", spec.namespace, err)
		}
	}
	return nil
}

// deleteManifest borra los recursos de un manifiesto multi-documento en ORDEN
// INVERSO al de aplicación (primero cargas, al final CRDs/roles). Los recursos
// que ya no existen se ignoran.
func (a *KubeActuator) deleteManifest(ctx context.Context, manifest []byte, defaultNS string) error {
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	var objs []*unstructured.Unstructured
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
		if obj.GetKind() != "" {
			objs = append(objs, obj)
		}
	}
	for i := len(objs) - 1; i >= 0; i-- {
		if err := a.deleteOne(ctx, objs[i], defaultNS); err != nil {
			return err
		}
	}
	return nil
}

func (a *KubeActuator) deleteOne(ctx context.Context, obj *unstructured.Unstructured, defaultNS string) error {
	gvk := obj.GroupVersionKind()
	mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil // el CRD ya no existe: sus recursos tampoco
	}
	var ri dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = defaultNS
		}
		ri = a.dyn.Resource(mapping.Resource).Namespace(ns)
	} else {
		ri = a.dyn.Resource(mapping.Resource)
	}
	if err := ri.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("borrando %s/%s: %w", gvk.Kind, obj.GetName(), err)
	}
	return nil
}

// unexpose retira una publicación creada por Atlas: borra el Ingress
// "atlas-<service>". Solo toca Ingress con la etiqueta managed-by=atlas — jamás
// borra rutas creadas por otros.
func (a *KubeActuator) unexpose(ctx context.Context, spec *api.ExposeSpec) error {
	if spec == nil {
		return fmt.Errorf("falta el servicio a despublicar")
	}
	name := "atlas-" + spec.Service
	ing, err := a.client.NetworkingV1().Ingresses(spec.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("el Ingress %s/%s no existe: %w", spec.Namespace, name, err)
	}
	if ing.Labels["app.kubernetes.io/managed-by"] != "atlas" {
		return fmt.Errorf("el Ingress %s/%s no lo gestiona Atlas: no lo toco", spec.Namespace, name)
	}
	return a.client.NetworkingV1().Ingresses(spec.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
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
