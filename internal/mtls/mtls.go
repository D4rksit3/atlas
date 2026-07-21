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
//
// La REVOCACIÓN es inmediata por el mismo mecanismo: si se pasa una CRL (lista de
// revocación firmada por la CA), cada handshake comprueba el serial de la hoja
// del par contra ella. La CRL también se recarga en caliente, así que revocar un
// agente (con `atlas-certs revoke`) surte efecto en el SIGUIENTE handshake sin
// reiniciar el control plane. Mientras la caducidad corta mitiga a medio plazo,
// la CRL corta el acceso al instante.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
)

// ServerTLSConfig construye la config del control plane: presenta su certificado
// y EXIGE + verifica el del cliente (agente) contra la CA. El certificado de
// servidor se recarga en caliente si cambia en disco. Si crlFile no está vacío,
// además rechaza en cada handshake a los agentes cuyo cert esté revocado.
func ServerTLSConfig(certFile, keyFile, clientCAFile, crlFile string) (*tls.Config, error) {
	rl, err := newReloader(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cargando certificado del servidor: %w", err)
	}
	pool, err := certPool(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CA de clientes: %w", err)
	}
	crl, err := newCRLChecker(crlFile, clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CRL: %w", err)
	}
	return &tls.Config{
		GetCertificate:        func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return rl.get() },
		ClientAuth:            tls.RequireAndVerifyClientCert,
		ClientCAs:             pool,
		VerifyPeerCertificate: crl.verifyPeer,
		MinVersion:            tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig construye la config del agente: presenta su certificado de
// cliente y verifica el del servidor contra la CA. El certificado de cliente se
// recarga en caliente si cambia en disco. Si crlFile no está vacío, además
// rechaza un certificado de servidor revocado.
func ClientTLSConfig(certFile, keyFile, caFile, crlFile string) (*tls.Config, error) {
	rl, err := newReloader(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cargando certificado del cliente: %w", err)
	}
	pool, err := certPool(caFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CA del servidor: %w", err)
	}
	crl, err := newCRLChecker(crlFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CRL: %w", err)
	}
	return &tls.Config{
		GetClientCertificate:  func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return rl.get() },
		RootCAs:               pool,
		VerifyPeerCertificate: crl.verifyPeer,
		MinVersion:            tls.VersionTLS13,
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
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%s no contiene certificados PEM válidos", caFile)
	}
	return pool, nil
}

// parseCerts lee todos los certificados PEM de un fichero (la CA puede traer más
// de un certificado, p. ej. durante una rotación de la CA).
func parseCerts(caFile string) ([]*x509.Certificate, error) {
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s no contiene certificados", caFile)
	}
	return certs, nil
}

// crlChecker rechaza handshakes cuyo certificado de par esté revocado. Mantiene el
// conjunto de seriales revocados recargado en caliente: cuando ca.crl cambia en
// disco, la revocación surte efecto en el SIGUIENTE handshake sin reiniciar. La
// CRL va firmada por la CA y se verifica su firma al cargarla, así un fichero
// manipulado no puede colar ni retirar revocaciones. Si crlFile está vacío, el
// checker queda deshabilitado (verifyPeer no hace nada).
type crlChecker struct {
	crlFile string
	cas     []*x509.Certificate // para verificar la firma de la CRL

	mu          sync.RWMutex
	revoked     map[string]bool // serial (base 10) -> revocado
	stamp       string          // firma del estado en disco del fichero CRL
	initialized bool
}

func newCRLChecker(crlFile, caFile string) (*crlChecker, error) {
	if crlFile == "" {
		return &crlChecker{}, nil // deshabilitado
	}
	cas, err := parseCerts(caFile)
	if err != nil {
		return nil, fmt.Errorf("leyendo CA para verificar la CRL: %w", err)
	}
	c := &crlChecker{crlFile: crlFile, cas: cas}
	// Carga inicial: si el fichero existe debe ser una CRL válida y firmada por la
	// CA; si aún no existe, arrancamos sin revocaciones (se cargará al aparecer).
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// verifyPeer es el callback VerifyPeerCertificate del tls.Config. Corre DESPUÉS
// de la verificación estándar (cadena + CA), así que verifiedChains[0][0] es la
// hoja del par ya validada. Rechaza si su serial está en la CRL.
func (c *crlChecker) verifyPeer(_ [][]byte, verifiedChains [][]*x509.Certificate) error {
	if c.crlFile == "" {
		return nil // revocación desactivada
	}
	if len(verifiedChains) == 0 || len(verifiedChains[0]) == 0 {
		return nil // sin cadena verificada no hay hoja que comprobar
	}
	if err := c.reload(); err != nil {
		return err // CRL ilegible/manipulada: fallamos cerrado (no confiamos)
	}
	leaf := verifiedChains[0][0]
	c.mu.RLock()
	revoked := c.revoked[leaf.SerialNumber.String()]
	c.mu.RUnlock()
	if revoked {
		return fmt.Errorf("certificado revocado: %q (serial %s)", leaf.Subject.CommonName, leaf.SerialNumber)
	}
	return nil
}

// reload relee la CRL si cambió en disco (mismo truco de mtime+size que la hoja).
func (c *crlChecker) reload() error {
	stamp := fileStamp(c.crlFile)

	c.mu.RLock()
	if c.initialized && stamp == c.stamp {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initialized && stamp == c.stamp {
		return nil
	}

	if stamp == "" { // el fichero no existe (aún): sin revocaciones
		if !c.initialized {
			c.revoked = map[string]bool{}
			c.initialized = true
		}
		// Si ANTES existía y ahora no, conservamos el último conjunto (no se
		// puede "des-revocar" borrando el fichero). No tocamos c.stamp para
		// reintentar la carga si el fichero reaparece.
		return nil
	}

	set, err := c.loadSet()
	if err != nil {
		if !c.initialized {
			return err // no hay set previo con el que seguir: es un fallo real
		}
		return nil // conserva el último conjunto válido ante un fichero corrupto
	}
	c.revoked = set
	c.stamp = stamp
	c.initialized = true
	return nil
}

// loadSet lee y verifica la CRL del disco y devuelve el conjunto de seriales
// revocados. Exige que la firme una de las CAs conocidas.
func (c *crlChecker) loadSet() (map[string]bool, error) {
	raw, err := os.ReadFile(c.crlFile)
	if err != nil {
		return nil, err
	}
	der := raw
	if block, _ := pem.Decode(raw); block != nil {
		der = block.Bytes
	}
	crl, err := x509.ParseRevocationList(der)
	if err != nil {
		return nil, fmt.Errorf("CRL ilegible: %w", err)
	}
	signed := false
	for _, ca := range c.cas {
		if crl.CheckSignatureFrom(ca) == nil {
			signed = true
			break
		}
	}
	if !signed {
		return nil, fmt.Errorf("la CRL no está firmada por la CA")
	}
	set := make(map[string]bool, len(crl.RevokedCertificateEntries))
	for _, e := range crl.RevokedCertificateEntries {
		set[e.SerialNumber.String()] = true
	}
	return set, nil
}

// fileStamp resume el estado en disco de un fichero. Cadena vacía si no existe.
func fileStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", st.ModTime().UnixNano(), st.Size())
}
