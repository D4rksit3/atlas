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
	"strconv"
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
		mustServerCert(fs.out, fs.hosts, fs.days)
	case "client":
		if fs.name == "" {
			fatal(fmt.Errorf("--name es obligatorio para 'client'"))
		}
		mustClientCert(fs.out, fs.name, fs.days)
	case "bundle":
		mustInitCA(fs.out)
		mustServerCert(fs.out, fs.hosts, fs.days)
		mustClientCert(fs.out, "agent", fs.days)
	case "revoke":
		mustRevoke(fs.out, fs.name, fs.cert, fs.serial)
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

func mustServerCert(out string, hosts []string, days int) {
	caCert, caKey := loadCA(out)
	key := mustKey()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "atlas-controlplane", Organization: []string{"Atlas"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     leafExpiry(days), // hoja de vida corta (rotación)
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
	fmt.Printf("✓ certificado de servidor para %s (válido %d días)\n", strings.Join(hosts, ", "), leafDays(days))
}

func mustClientCert(out, name string, days int) {
	caCert, caKey := loadCA(out)
	key := mustKey()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		// El CN identifica al agente (útil para trazas/autorización futura).
		Subject:     pkix.Name{CommonName: name, Organization: []string{"Atlas Agents"}},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    leafExpiry(days),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	must(err)
	writeCert(filepath.Join(out, name+".crt"), der)
	writeKey(filepath.Join(out, name+".key"), key)
	fmt.Printf("✓ certificado de cliente para el agente %q (válido %d días)\n", name, leafDays(days))
}

// mustRevoke añade el serial de un certificado a la CRL (<out>/ca.crl), firmada
// por la CA. La CRL se regenera acumulando las revocaciones previas + la nueva y
// subiendo el número de CRL. El control plane/agente la recargan en caliente, así
// que la revocación surte efecto en el siguiente handshake sin reiniciar.
//
// El serial se obtiene de --cert PATH, de --name NOMBRE (<out>/NOMBRE.crt) o de
// --serial (decimal o 0xHEX).
func mustRevoke(out, name, certPath, serialStr string) {
	caCert, caKey := loadCA(out)

	serial := revokeSerial(out, name, certPath, serialStr)

	crlPath := filepath.Join(out, "ca.crl")
	entries, number := loadCRL(crlPath, caCert)

	// No duplicar un serial ya revocado.
	for _, e := range entries {
		if e.SerialNumber.Cmp(serial) == 0 {
			fmt.Printf("• el serial %s ya estaba revocado (CRL sin cambios)\n", serial)
			return
		}
	}
	entries = append(entries, x509.RevocationListEntry{
		SerialNumber:   serial,
		RevocationTime: time.Now().UTC(),
	})

	tmpl := &x509.RevocationList{
		Number:                    number.Add(number, big.NewInt(1)),
		ThisUpdate:                time.Now().Add(-time.Hour),
		NextUpdate:                time.Now().AddDate(10, 0, 0), // larga: la refrescamos al revocar
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, caCert, caKey)
	must(err)
	buf := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	must(os.WriteFile(crlPath, buf, 0o644))
	fmt.Printf("✓ serial %s revocado — CRL con %d revocación(es) en %s\n", serial, len(entries), crlPath)
}

// revokeSerial resuelve el serial a revocar desde --cert, --name o --serial.
func revokeSerial(out, name, certPath, serialStr string) *big.Int {
	switch {
	case serialStr != "":
		s := new(big.Int)
		base := 10
		if strings.HasPrefix(serialStr, "0x") || strings.HasPrefix(serialStr, "0X") {
			serialStr, base = serialStr[2:], 16
		}
		if _, ok := s.SetString(serialStr, base); !ok {
			fatal(fmt.Errorf("--serial no es un entero válido: %q", serialStr))
		}
		return s
	case certPath != "":
		return serialFromCert(certPath)
	case name != "":
		return serialFromCert(filepath.Join(out, name+".crt"))
	default:
		fatal(fmt.Errorf("revoke necesita --cert PATH, --name NOMBRE o --serial N"))
		return nil
	}
}

func serialFromCert(path string) *big.Int {
	pemBytes, err := os.ReadFile(path)
	must(err)
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		fatal(fmt.Errorf("%s no es un certificado PEM", path))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	must(err)
	return cert.SerialNumber
}

// loadCRL lee las revocaciones previas de la CRL (si existe) y su número. Verifica
// que esté firmada por la CA antes de acumular sobre ella.
func loadCRL(path string, caCert *x509.Certificate) ([]x509.RevocationListEntry, *big.Int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, big.NewInt(0) // primera revocación: CRL nueva
	}
	der := raw
	if block, _ := pem.Decode(raw); block != nil {
		der = block.Bytes
	}
	crl, err := x509.ParseRevocationList(der)
	must(err)
	if err := crl.CheckSignatureFrom(caCert); err != nil {
		fatal(fmt.Errorf("la CRL existente en %s no está firmada por esta CA: %w", path, err))
	}
	return crl.RevokedCertificateEntries, crl.Number
}

// leafDays devuelve los días de validez de una hoja (default 90 si no se indica).
func leafDays(days int) int {
	if days <= 0 {
		return 90
	}
	return days
}

// leafExpiry es la fecha de expiración de una hoja: certs cortos para forzar
// rotación frecuente (con el hot-reload de internal/mtls no requiere reinicio).
func leafExpiry(days int) time.Time { return time.Now().AddDate(0, 0, leafDays(days)) }

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
	out    string
	hosts  []string
	name   string
	days   int
	cert   string
	serial string
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
		case "--days":
			i++
			d, err := strconv.Atoi(arg(args, i))
			if err != nil || d <= 0 {
				return fmt.Errorf("--days debe ser un entero positivo (días de validez de la hoja)")
			}
			f.days = d
		case "--cert":
			i++
			f.cert = arg(args, i)
		case "--serial":
			i++
			f.serial = arg(args, i)
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
  atlas-certs server --out DIR --hosts host1,host2,ip [--days N]
  atlas-certs client --out DIR --name NOMBRE [--days N]
  atlas-certs bundle --out DIR --hosts host1,ip [--days N]   (init + server + un cliente)
  atlas-certs revoke --out DIR (--name NOMBRE | --cert PATH | --serial N)

  --days    validez de la hoja (server/cliente), default 90. La CA dura 10 años.
            Certs cortos + hot-reload del control plane/agente = rotación sin reinicio.
  revoke    añade el cert a <out>/ca.crl (CRL firmada por la CA). El control plane
            la recarga en caliente: el agente revocado queda fuera en el acto, sin
            reiniciar. Pásasela con --tls-crl <out>/ca.crl.`)
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
