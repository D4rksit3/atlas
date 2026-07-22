// Package pki emite certificados de cliente firmados por la CA de Atlas.
//
// Es el corazón compartido entre dos emisores:
//   - cmd/atlas-certs (CLI): el flujo "estricto" con la CA offline.
//   - el control plane (enrolamiento por token): emite el cert del agente al
//     vuelo cuando un token de vinculación válido lo pide. Para esto la CA (o
//     una intermedia) se monta en el control plane — trade-off elegido a
//     cambio de vincular clústeres con UN comando; lo mitigan los tokens de un
//     solo uso con caducidad, la auditoría, las hojas cortas y la CRL.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"
)

// CA es una autoridad certificadora cargada en memoria, lista para firmar.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey

	certPEM []byte // el PEM original, para incluirlo en bundles
}

// LoadCA lee la CA desde ficheros PEM (cert + clave). Verifica que casan.
func LoadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("leyendo cert de la CA: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("leyendo clave de la CA: %w", err)
	}
	return ParseCA(certPEM, keyPEM)
}

// ParseCA construye la CA desde PEM en memoria.
func ParseCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, errors.New("el cert de la CA no es PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parseando cert de la CA: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("el certificado no es una CA")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errors.New("la clave de la CA no es PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parseando clave de la CA: %w", err)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("la clave privada no corresponde al cert de la CA")
	}
	return &CA{Cert: cert, Key: key, certPEM: certPEM}, nil
}

// CertPEM devuelve el certificado de la CA en PEM (para que los clientes
// verifiquen al servidor / el servidor a los clientes).
func (c *CA) CertPEM() []byte { return c.certPEM }

// IssueClient emite un certificado de CLIENTE (mTLS de agente) firmado por la
// CA, con el nombre como CN. days<=0 usa 90 (hojas cortas por defecto, como
// atlas-certs). Devuelve cert y clave en PEM.
func (c *CA) IssueClient(name string, days int) (certPEM, keyPEM []byte, err error) {
	if name == "" {
		return nil, nil, errors.New("el certificado necesita un nombre (CN)")
	}
	if days <= 0 {
		days = 90
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		// El CN identifica al agente (útil para trazas/autorización futura).
		Subject:     pkix.Name{CommonName: name, Organization: []string{"Atlas Agents"}},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().AddDate(0, 0, days),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &key.PublicKey, c.Key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
