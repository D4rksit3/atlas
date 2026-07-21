package agent

import (
	"context"
	"fmt"
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

// installHelm instala un chart de Helm VETADO como un release real. Usa el SDK de
// Helm compilado dentro del agente (no requiere el binario 'helm'), y funciona
// igual in-cluster o con kubeconfig porque parte del rest.Config del agente.
func (a *KubeActuator) installHelm(ctx context.Context, namespace string, c *helmChart) error {
	getter := &restGetter{cfg: a.cfg, namespace: namespace}
	cfg := new(action.Configuration)
	if err := cfg.Init(getter, namespace, "secret", func(string, ...interface{}) {}); err != nil {
		return fmt.Errorf("inicializando Helm: %w", err)
	}

	inst := action.NewInstall(cfg)
	inst.ReleaseName = c.release
	inst.Namespace = namespace
	inst.CreateNamespace = true
	inst.RepoURL = c.repo
	inst.Version = c.version
	inst.Timeout = 4 * time.Minute
	inst.Wait = false

	settings := cli.New()
	chartPath, err := inst.ChartPathOptions.LocateChart(c.chart, settings)
	if err != nil {
		return fmt.Errorf("localizando el chart %s: %w", c.chart, err)
	}
	ch, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("cargando el chart: %w", err)
	}
	vals := c.values
	if vals == nil {
		vals = map[string]interface{}{}
	}
	if _, err := inst.RunWithContext(ctx, ch, vals); err != nil {
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
