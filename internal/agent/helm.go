package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	memcache "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// installHelm instala (o ACTUALIZA, si ya existe el release) un chart de Helm
// VETADO como un release real. Usa el SDK de Helm compilado dentro del agente (no
// requiere el binario 'helm') y funciona igual in-cluster o con kubeconfig.
func (a *KubeActuator) installHelm(ctx context.Context, namespace string, c *helmChart, values map[string]interface{}) error {
	getter := &restGetter{cfg: a.cfg, namespace: namespace}
	cfg := new(action.Configuration)
	if err := cfg.Init(getter, namespace, "secret", func(string, ...interface{}) {}); err != nil {
		return fmt.Errorf("inicializando Helm: %w", err)
	}
	if values == nil {
		values = map[string]interface{}{}
	}
	settings := cli.New()

	// ¿Ya existe el release? -> upgrade; si no -> install.
	hist, herr := action.NewHistory(cfg).Run(c.release)
	if herr == nil && len(hist) > 0 {
		up := action.NewUpgrade(cfg)
		up.Namespace = namespace
		up.RepoURL = c.repo
		up.Version = c.version
		up.Timeout = 5 * time.Minute
		up.Wait = false
		up.ReuseValues = true // conserva los valores previos; solo sobreescribe los editados
		chartPath, err := up.ChartPathOptions.LocateChart(c.chart, settings)
		if err != nil {
			return fmt.Errorf("localizando el chart %s: %w", c.chart, err)
		}
		ch, err := loader.Load(chartPath)
		if err != nil {
			return fmt.Errorf("cargando el chart: %w", err)
		}
		if _, err := up.RunWithContext(ctx, c.release, ch, values); err != nil {
			return fmt.Errorf("actualizando %s: %w", c.chart, err)
		}
		return nil
	}

	inst := action.NewInstall(cfg)
	inst.ReleaseName = c.release
	inst.Namespace = namespace
	inst.CreateNamespace = true
	inst.RepoURL = c.repo
	inst.Version = c.version
	inst.Timeout = 5 * time.Minute
	inst.Wait = false

	chartPath, err := inst.ChartPathOptions.LocateChart(c.chart, settings)
	if err != nil {
		return fmt.Errorf("localizando el chart %s: %w", c.chart, err)
	}
	ch, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("cargando el chart: %w", err)
	}
	if _, err := inst.RunWithContext(ctx, ch, values); err != nil {
		return fmt.Errorf("instalando %s: %w", c.chart, err)
	}
	return nil
}

// restGetter adapta un *rest.Config al RESTClientGetter que espera Helm.
type restGetter struct {
	cfg       *rest.Config
	namespace string
}

func (g *restGetter) ToRESTConfig() (*rest.Config, error) { return g.cfg, nil }

func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return memcache.NewMemCacheClient(dc), nil
}

func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}

func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	// Helm lo usa sobre todo para resolver el namespace por defecto.
	overrides := &clientcmd.ConfigOverrides{Context: clientcmdapi.Context{Namespace: g.namespace}}
	return clientcmd.NewDefaultClientConfig(*clientcmdapi.NewConfig(), overrides)
}

// uninstallHelm desinstala un release de Helm del catálogo. Si el release no
// existe, no es un error (idempotente).
func (a *KubeActuator) uninstallHelm(namespace string, c *helmChart) error {
	getter := &restGetter{cfg: a.cfg, namespace: namespace}
	cfg := new(action.Configuration)
	if err := cfg.Init(getter, namespace, "secret", func(string, ...interface{}) {}); err != nil {
		return fmt.Errorf("inicializando Helm: %w", err)
	}
	un := action.NewUninstall(cfg)
	un.Timeout = 5 * time.Minute
	if _, err := un.Run(c.release); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil // ya no estaba: objetivo cumplido
		}
		return fmt.Errorf("desinstalando %s: %w", c.release, err)
	}
	return nil
}
