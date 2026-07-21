// Command atlas-certs genera una PKI mínima para el mTLS de Atlas: una CA raíz,
// el certificado del control plane (servidor) y certificados de cliente para los
// agentes. Todo con ECDSA P-256, sin dependencias externas.
//
// Uso típico (desarrollo o arranque):
//
//	atlas-certs init   --out certs
//	atlas-certs server --out certs --hosts localhost,atlas-controlplane.atlas-system,127.0.0.1
//	atlas-certs client --out certs --name prod-eks
//
// O todo de una:
//
//	atlas-certs bundle --out certs --hosts localhost,127.0.0.1
//
// Los ficheros quedan en <out>/: ca.crt, ca.key, server.crt, server.key,
// <name>.crt, <name>.key. Guarda las *.key con cuidado (se escriben con 0600).
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := newFlags(cmd)
	if err := fs.parse(os.Args[2:]); err != nil {
		fatal(err)
	}

	switch cmd {
	case "init":
		mustInitCA(fs.out)
	case "server":
		mustServerCert(fs.out, fs.hosts)
	case "client":
		if fs.name == "" {
			fatal(fmt.Errorf("--name es obligatorio para 'client'"))
		}
		mustClientCert(fs.out, fs.name)
	case "bundle":
		mustInitCA(fs.out)
		mustServerCert(fs.out, fs.hosts)
		mustClientCert(fs.out, "agent")
	default:
		usage()
	}
}

// ---- subcomandos ----

func mustInitCA(out string) {
	mustMkdir(out)
	key := mustKey()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Atlas Root CA", Organization: []string{"Atlas"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0), // CA larga: 10 años
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(err)
	writeCert(filepath.Join(out, "ca.crt"), der)
	writeKey(filepath.Join(out, "ca.key"), key)
	fmt.Printf("✓ CA creada en %s/ca.crt\n", out)
}

func mustServerCert(out string, hosts []string) {
	caCert, caKey := loadCA(out)
	key := mustKey()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "atlas-controlplane", Organization: []string{"Atlas"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0), // hoja: 1 año
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost", "127.0.0.1"}
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	must(err)
	writeCert(filepath.Join(out, "server.crt"), der)
	writeKey(filepath.Join(out, "server.key"), key)
	fmt.Printf("✓ certificado de servidor para %s\n", strings.Join(hosts, ", "))
}

func mustClientCert(out, name string) {
	caCert, caKey := loadCA(out)
	key := mustKey()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		// El CN identifica al agente (útil para trazas/autorización futura).
		Subject:     pkix.Name{CommonName: name, Organization: []string{"Atlas Agents"}},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	must(err)
	writeCert(filepath.Join(out, name+".crt"), der)
	writeKey(filepath.Join(out, name+".key"), key)
	fmt.Printf("✓ certificado de cliente para el agente %q\n", name)
}

// ---- helpers de PKI ----

func loadCA(out string) (*x509.Certificate, *ecdsa.PrivateKey) {
	certPEM, err := os.ReadFile(filepath.Join(out, "ca.crt"))
	must(err)
	keyPEM, err := os.ReadFile(filepath.Join(out, "ca.key"))
	must(err)
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		fatal(fmt.Errorf("CA ilegible en %s (¿ejecutaste 'init'?)", out))
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	must(err)
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	must(err)
	return cert, key
}

func mustKey() *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	return key
}

func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	must(err)
	return n
}

func writeCert(path string, der []byte) {
	buf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	must(os.WriteFile(path, buf, 0o644))
}

func writeKey(path string, key *ecdsa.PrivateKey) {
	der, err := x509.MarshalECPrivateKey(key)
	must(err)
	buf := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	must(os.WriteFile(path, buf, 0o600)) // clave privada: solo el dueño
}

func mustMkdir(dir string) { must(os.MkdirAll(dir, 0o755)) }

// ---- flags mínimos (sin dependencias) ----

type flags struct {
	out   string
	hosts []string
	name  string
}

func newFlags(cmd string) *flags { return &flags{out: "certs"} }

func (f *flags) parse(args []string) error {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			i++
			f.out = arg(args, i)
		case "--hosts":
			i++
			f.hosts = splitCSV(arg(args, i))
		case "--name":
			i++
			f.name = arg(args, i)
		default:
			return fmt.Errorf("opción desconocida: %s", args[i])
		}
	}
	return nil
}

func arg(a []string, i int) string {
	if i >= len(a) {
		fatal(fmt.Errorf("falta el valor de %s", a[i-1]))
	}
	return a[i]
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func usage() {
	fmt.Fprintln(os.Stderr, `atlas-certs — PKI mínima para el mTLS de Atlas

  atlas-certs init   --out DIR
  atlas-certs server --out DIR --hosts host1,host2,ip
  atlas-certs client --out DIR --name NOMBRE
  atlas-certs bundle --out DIR --hosts host1,ip   (init + server + un cliente)`)
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "atlas-certs: "+err.Error())
	os.Exit(1)
}
