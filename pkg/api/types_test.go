package api

import "testing"

// TestACMEDirectory fija el catálogo CERRADO de servidores ACME: solo staging y
// production resuelven; cualquier otra cosa (incluida una URL arbitraria) se
// rechaza. Es lo que evita que la GUI apunte el emisor a un ACME cualquiera.
func TestACMEDirectory(t *testing.T) {
	cases := map[string]bool{
		"staging":                        true,
		"production":                     true,
		"":                               false,
		"prod":                           false,
		"https://evil.example/directory": false,
		"STAGING":                        false,
	}
	for env, wantOK := range cases {
		url, ok := ACMEDirectory(env)
		if ok != wantOK {
			t.Errorf("ACMEDirectory(%q): ok=%v, quiero %v", env, ok, wantOK)
		}
		if ok && url == "" {
			t.Errorf("ACMEDirectory(%q): ok pero URL vacía", env)
		}
		if !ok && url != "" {
			t.Errorf("ACMEDirectory(%q): rechazado pero devolvió URL %q", env, url)
		}
	}
}

func TestIssuerDefaults(t *testing.T) {
	// Nombre por defecto derivado del entorno.
	if got := (IssuerSpec{Environment: "staging"}).IssuerName(); got != "letsencrypt-staging" {
		t.Errorf("IssuerName default = %q, quiero letsencrypt-staging", got)
	}
	// Nombre explícito manda.
	if got := (IssuerSpec{Name: "mi-emisor", Environment: "production"}).IssuerName(); got != "mi-emisor" {
		t.Errorf("IssuerName explícito = %q, quiero mi-emisor", got)
	}
	// Clase de Ingress por defecto = nginx.
	if got := (IssuerSpec{}).IngressClassOr(); got != "nginx" {
		t.Errorf("IngressClassOr default = %q, quiero nginx", got)
	}
	if got := (IssuerSpec{IngressClass: "traefik"}).IngressClassOr(); got != "traefik" {
		t.Errorf("IngressClassOr explícito = %q, quiero traefik", got)
	}
}
