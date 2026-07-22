package agent

import (
	"testing"

	"github.com/atlasctl/atlas/pkg/api"
)

// TestAddonCatalogConsistente evita el error fácil de añadir un complemento al
// catálogo de la GUI (api.Addons) y olvidar su chart/manifiesto en el agente (o al
// revés). Si no cuadran, "Instalar" fallaría en tiempo de ejecución; aquí falla en
// tiempo de test. También comprueba invariantes básicos de cada entrada.
func TestAddonCatalogConsistente(t *testing.T) {
	// Cada complemento que ve la GUI debe existir en el agente, con el mismo
	// namespace y con exactamente una vía de instalación (manifiesto XOR chart).
	for _, info := range api.Addons() {
		spec, ok := addons[info.Key]
		if !ok {
			t.Errorf("%q está en api.Addons() pero no en el catálogo del agente (Instalar fallaría)", info.Key)
			continue
		}
		if spec.namespace != info.Namespace {
			t.Errorf("%q: namespace GUI=%q vs agente=%q (deben coincidir)", info.Key, info.Namespace, spec.namespace)
		}
		hasURL := spec.url != ""
		hasHelm := spec.helm != nil
		if hasURL == hasHelm {
			t.Errorf("%q: debe tener manifiesto (url) O chart de Helm, no ambos ni ninguno", info.Key)
		}
		if hasHelm && spec.helm.version == "" {
			t.Errorf("%q: el chart de Helm debe tener versión fijada (cadena de confianza)", info.Key)
		}
		// Cada Param editable debe apuntar a una ruta de values (path) no vacía.
		for _, p := range info.Params {
			if p.Path == "" {
				t.Errorf("%q: el parámetro %q no tiene path de values (no se aplicaría)", info.Key, p.Key)
			}
		}
	}

	// Y a la inversa: nada instalable en el agente que la GUI no muestre (evita
	// complementos "fantasma" sin metadatos).
	for key := range addons {
		found := false
		for _, info := range api.Addons() {
			if info.Key == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q está en el agente pero no en api.Addons() (la GUI no lo mostraría)", key)
		}
	}
}

// TestMergeAddonValuesNoMuta verifica que aplicar valores de usuario NO muta los
// values base del catálogo compartido (mergeAddonValues debe copiar en profundo):
// si mutara, la contraseña de una instalación quedaría pegada en el mapa global.
func TestMergeAddonValuesNoMuta(t *testing.T) {
	spec := addons["kube-prometheus-stack"]
	if spec.helm == nil || spec.helm.values == nil {
		t.Fatal("kube-prometheus-stack debería tener values base (allow_embedding)")
	}
	out := mergeAddonValues("kube-prometheus-stack", spec.helm.values,
		map[string]string{"grafanaPassword": "super-secreta"})

	// El resultado lleva el valor del usuario en el path vetado…
	g, _ := out["grafana"].(map[string]interface{})
	if g == nil || g["adminPassword"] != "super-secreta" {
		t.Fatalf("el valor del usuario no se aplicó: %#v", out)
	}
	// …y conserva el base value de embedding…
	ini, _ := g["grafana.ini"].(map[string]interface{})
	sec, _ := ini["security"].(map[string]interface{})
	if sec == nil || sec["allow_embedding"] != true {
		t.Fatalf("se perdió el base value allow_embedding: %#v", out)
	}
	// …pero el mapa BASE del catálogo queda intacto (sin la contraseña).
	baseG, _ := spec.helm.values["grafana"].(map[string]interface{})
	if _, leaked := baseG["adminPassword"]; leaked {
		t.Fatal("mergeAddonValues mutó los values base compartidos (fuga de contraseña)")
	}
}
