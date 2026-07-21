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

	cfg, err := ServerTLSConfig(certFile, keyFile, caFile, "")
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

	serverCfg, err := ServerTLSConfig(certFile, keyFile, caFile, "")
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

// TestRevocationOverSocket es la prueba de verdad de la revocación INMEDIATA: un
// servidor TLS real que exige cert de cliente y comprueba una CRL. El cliente
// (agente) conecta bien; luego se escribe una CRL firmada por la CA que revoca su
// serial; el SIGUIENTE handshake se rechaza — sin reconstruir el tls.Config ni
// reiniciar el servidor. Eso es lo que da la revocación en el acto.
func TestRevocationOverSocket(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := testCA(t)

	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	crlFile := filepath.Join(dir, "ca.crl")
	writeCertPEM(t, caFile, caCert.Raw)
	writePair(t, certFile, keyFile, caCert, caKey, "atlas-controlplane")

	// Cert de cliente (agente) con un serial conocido, firmado por la misma CA.
	clientCertFile := filepath.Join(dir, "agent.crt")
	clientKeyFile := filepath.Join(dir, "agent.key")
	clientSerial := big.NewInt(0xA11A5)
	clientCert := writeClientPair(t, clientCertFile, clientKeyFile, caCert, caKey, "prod-eks", clientSerial)

	// El servidor exige y verifica cert de cliente, y consulta la CRL (aún ausente).
	serverCfg, err := ServerTLSConfig(certFile, keyFile, caFile, crlFile)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				tc := conn.(*tls.Conn)
				if err := tc.Handshake(); err != nil {
					return // handshake rechazado (p. ej. cert revocado): no respondemos
				}
				// Un byte de vuelta para que el cliente confirme el handshake OK.
				buf := make([]byte, 1)
				if _, err := tc.Read(buf); err != nil {
					return
				}
				_, _ = tc.Write(buf)
			}()
		}
	}()

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	// En TLS 1.3 el Handshake() del cliente termina antes de que el servidor valide
	// su certificado; el rechazo (cert revocado) llega como alerta en la primera
	// lectura. Por eso hacemos un round-trip de 1 byte: si el servidor aceptó, vuelve
	// el eco; si revocó, el Read/Write devuelve error.
	dial := func() error {
		conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			RootCAs:      roots,
			Certificates: []tls.Certificate{clientCert},
			ServerName:   "localhost",
		})
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte{1}); err != nil {
			return err
		}
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err != nil {
			return err
		}
		return nil
	}

	// Sin CRL todavía: el agente conecta.
	if err := dial(); err != nil {
		t.Fatalf("handshake inicial (sin revocar): %v", err)
	}

	// Revoca el serial del agente: CRL firmada por la CA escrita en disco.
	writeCRL(t, crlFile, caCert, caKey, clientSerial)
	touchFuture(t, crlFile)

	// El SIGUIENTE handshake debe rechazarse, sin reiniciar el servidor.
	if err := dial(); err == nil {
		t.Fatal("el agente revocado siguió conectando (la CRL no cortó el acceso)")
	}

	// Un agente NO revocado (otro serial) debe seguir entrando.
	otherCertFile := filepath.Join(dir, "other.crt")
	otherKeyFile := filepath.Join(dir, "other.key")
	otherCert := writeClientPair(t, otherCertFile, otherKeyFile, caCert, caKey, "prod-oci", big.NewInt(0xB0B))
	clientCert = otherCert
	if err := dial(); err != nil {
		t.Fatalf("un agente NO revocado fue rechazado: %v", err)
	}
}

// writeClientPair emite un cert de cliente (ExtKeyUsage ClientAuth) con un serial
// concreto y lo escribe a disco; devuelve el tls.Certificate para el dial.
func writeClientPair(t *testing.T, certFile, keyFile string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, serial *big.Int) tls.Certificate {
	t.Helper()
	key := mustGenKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, 90),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
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
	crt, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return crt
}

// writeCRL escribe una CRL PEM firmada por la CA que revoca los seriales dados.
func writeCRL(t *testing.T, path string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serials ...*big.Int) {
	t.Helper()
	var entries []x509.RevocationListEntry
	for _, s := range serials {
		entries = append(entries, x509.RevocationListEntry{SerialNumber: s, RevocationTime: time.Now().UTC()})
	}
	tmpl := &x509.RevocationList{
		Number:                    big.NewInt(1),
		ThisUpdate:                time.Now().Add(-time.Hour),
		NextUpdate:                time.Now().AddDate(1, 0, 0),
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, ca, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
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
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
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
