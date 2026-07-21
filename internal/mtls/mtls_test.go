package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServerCertHotReload verifica que ServerTLSConfig recarga el certificado de
// servidor cuando cambia en disco, SIN reconstruir el tls.Config: el segundo
// GetCertificate devuelve el nuevo cert tras reemplazar los ficheros.
func TestServerCertHotReload(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := testCA(t)

	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	writePair(t, certFile, keyFile, caCert, caKey, "atlas-controlplane-v1")

	caFile := filepath.Join(dir, "ca.crt")
	writeCertPEM(t, caFile, caCert.Raw)

	cfg, err := ServerTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	first := leafCN(t, cfg)
	if first != "atlas-controlplane-v1" {
		t.Fatalf("CN inicial = %q, quiero atlas-controlplane-v1", first)
	}

	// Reemplaza el certificado en disco (rotación) con un mtime distinto.
	time.Sleep(10 * time.Millisecond)
	writePair(t, certFile, keyFile, caCert, caKey, "atlas-controlplane-v2")
	touchFuture(t, certFile)
	touchFuture(t, keyFile)

	second := leafCN(t, cfg)
	if second != "atlas-controlplane-v2" {
		t.Fatalf("tras rotar, CN = %q, quiero atlas-controlplane-v2 (no se recargó)", second)
	}
}

// TestHotReloadOverSocket es la prueba de verdad: un servidor TLS real con la
// config de ServerTLSConfig, un cliente que hace el handshake completo, se rota
// el certificado del servidor en disco y un SEGUNDO handshake ve el cert nuevo —
// todo sin reconstruir el tls.Config ni reiniciar el "servidor". Esto es lo que
// hace viable la rotación con hojas de vida corta.
func TestHotReloadOverSocket(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := testCA(t)

	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	writeCertPEM(t, caFile, caCert.Raw)
	writePair(t, certFile, keyFile, caCert, caKey, "atlas-controlplane-v1")

	serverCfg, err := ServerTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	// El servidor no exige cliente aquí (probamos la hoja del servidor).
	serverCfg.ClientAuth = tls.NoClientCert

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Acepta conexiones en bucle: cada una completa el handshake y cierra.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			tc := conn.(*tls.Conn)
			_ = tc.Handshake()
			tc.Close()
		}
	}()

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	dialCN := func() string {
		t.Helper()
		conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "localhost"})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
	}

	if cn := dialCN(); cn != "atlas-controlplane-v1" {
		t.Fatalf("handshake inicial: CN = %q, quiero atlas-controlplane-v1", cn)
	}

	// Rota el cert del servidor en disco (como haría cert-manager al renovar).
	writePair(t, certFile, keyFile, caCert, caKey, "atlas-controlplane-v2")
	touchFuture(t, certFile)
	touchFuture(t, keyFile)

	if cn := dialCN(); cn != "atlas-controlplane-v2" {
		t.Fatalf("tras rotar: CN = %q, quiero atlas-controlplane-v2 (no se recargó en caliente)", cn)
	}
}

// leafCN ejecuta el callback GetCertificate del tls.Config y devuelve el CN de la
// hoja que presentaría el servidor.
func leafCN(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	if cfg.GetCertificate == nil {
		t.Fatal("GetCertificate es nil (no hay hot-reload)")
	}
	c, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return leaf.Subject.CommonName
}

func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key := mustGenKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func writePair(t *testing.T, certFile, keyFile string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) {
	t.Helper()
	key := mustGenKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, 90),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	writeCertPEM(t, certFile, der)
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCertPEM(t *testing.T, path string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

// touchFuture adelanta el mtime para garantizar que diskStamp cambia aunque la
// resolución del reloj del sistema de ficheros sea gruesa.
func touchFuture(t *testing.T, path string) {
	t.Helper()
	ts := time.Now().Add(time.Second)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func mustGenKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
