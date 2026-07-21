// Package mtls arma las configuraciones TLS mutuas (mTLS) del control plane y el
// agente. El agente presenta un certificado de cliente firmado por la CA de
// Atlas; el control plane lo exige y lo verifica. A la vez, el agente verifica
// el certificado del servidor contra la misma CA. Así ningún extremo confía en
// nadie sin certificado válido — reemplaza al token placeholder como identidad.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ServerTLSConfig construye la config del control plane: presenta su certificado
// y EXIGE + verifica el del cliente (agente) contra la CA.
func ServerTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cargando certificado del servidor: %w", err)
	}
	pool, err := certPool(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CA de clientes: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig construye la config del agente: presenta su certificado de
// cliente y verifica el del servidor contra la CA.
func ClientTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cargando certificado del cliente: %w", err)
	}
	pool, err := certPool(caFile)
	if err != nil {
		return nil, fmt.Errorf("cargando CA del servidor: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
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
