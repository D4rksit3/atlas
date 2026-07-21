// Package mtls arma las configuraciones TLS mutuas (mTLS) del control plane y el
// agente. El agente presenta un certificado de cliente firmado por la CA de
// Atlas; el control plane lo exige y lo verifica. A la vez, el agente verifica
// el certificado del servidor contra la misma CA. Así ningún extremo confía en
// nadie sin certificado válido — reemplaza al token placeholder como identidad.
//
// Los certificados de hoja se recargan en caliente: cuando el fichero cambia en
// disco (p. ej. cert-manager renueva el Secret montado, o se re-emite con
// atlas-certs), el nuevo certificado se usa en el siguiente handshake SIN
// reiniciar el proceso. Esto es lo que hace viable la rotación con expiración
// corta.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
)

// ServerTLSConfig construye la config del control plane: presenta su certificado
// y EXIGE + verifica el del cliente (agente) contra la CA. El certificado de
// servidor se recarga en caliente si cambia en disco.
func ServerTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	rl, err := newReloader(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cargando certificado del servidor: %w", err)
	}
	pool, err := certPool(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CA de clientes: %w", err)
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return rl.get() },
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool,
		MinVersion:     tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig construye la config del agente: presenta su certificado de
// cliente y verifica el del servidor contra la CA. El certificado de cliente se
// recarga en caliente si cambia en disco.
func ClientTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	rl, err := newReloader(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cargando certificado del cliente: %w", err)
	}
	pool, err := certPool(caFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CA del servidor: %w", err)
	}
	return &tls.Config{
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return rl.get() },
		RootCAs:              pool,
		MinVersion:           tls.VersionTLS13,
	}, nil
}

// certReloader relee el par cert/key del disco cuando cambia (mtime o tamaño),
// cacheando el keypair parseado. os.Stat por handshake es barato; solo se vuelve
// a leer y parsear el PEM cuando el fichero cambia de verdad.
type certReloader struct {
	certFile, keyFile string
	mu                sync.RWMutex
	cached            *tls.Certificate
	stamp             string // firma del estado en disco (mtime+size de cert y key)
}

func newReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.get(); err != nil { // carga inicial (falla rápido si no existen)
		return nil, err
	}
	return r, nil
}

func (r *certReloader) get() (*tls.Certificate, error) {
	stamp := r.diskStamp()
	r.mu.RLock()
	if r.cached != nil && stamp != "" && stamp == r.stamp {
		c := r.cached
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached != nil && stamp != "" && stamp == r.stamp { // recarga concurrente
		return r.cached, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cached != nil {
			return r.cached, nil // si la recarga falla, seguimos con el último válido
		}
		return nil, err
	}
	r.cached = &cert
	r.stamp = stamp
	return r.cached, nil
}

// diskStamp resume el estado en disco de ambos ficheros. Cadena vacía si no se
// pueden leer (get() usará el cacheado).
func (r *certReloader) diskStamp() string {
	cs, err1 := os.Stat(r.certFile)
	ks, err2 := os.Stat(r.keyFile)
	if err1 != nil || err2 != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d-%d-%d", cs.ModTime().UnixNano(), cs.Size(), ks.ModTime().UnixNano(), ks.Size())
}

func certPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s no contiene certificados PEM válidos", caFile)
	}
	return pool, nil
}
