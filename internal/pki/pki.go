// Package pki makes the private CA and the wildcard certificate that the pooka
// proxy serves (ADR 0008). The files live under one directory on the host:
// ca.pem, ca.key, cert.pem, key.pem. Delete the directory to start over.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 397 * 24 * time.Hour // the longest browsers accept
	renewBefore  = 30 * 24 * time.Hour
)

// Names are the DNS names a certificate for a domain must carry.
func Names(domain string) []string {
	return []string{"*." + domain, "api." + domain, domain}
}

// CAFile returns the path of the CA certificate under dir.
func CAFile(dir string) string { return filepath.Join(dir, "ca.pem") }

// EnsureCert returns a certificate for Names(domain), signed by the CA under dir.
// It creates the CA when dir has none, and issues a new leaf when the current
// one is missing, carries other names, or has less than 30 days left.
func EnsureCert(dir, domain string, now time.Time) (tls.Certificate, error) {
	if domain == "" {
		return tls.Certificate{}, errors.New("pki: domain is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	caCert, caKey, err := loadOrCreateCA(dir, now)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil && leafCurrent(leaf, caCert, domain, now) {
			return cert, nil
		}
	}
	return issueLeaf(certPath, keyPath, caCert, caKey, domain, now)
}

// leafCurrent reports whether the leaf serves the domain, was signed by this
// CA, and has more than renewBefore left.
func leafCurrent(leaf, ca *x509.Certificate, domain string, now time.Time) bool {
	if leaf.CheckSignatureFrom(ca) != nil {
		return false
	}
	for _, n := range Names(domain) {
		if !slices.Contains(leaf.DNSNames, n) {
			return false
		}
	}
	return now.Add(renewBefore).Before(leaf.NotAfter) && !now.Before(leaf.NotBefore)
}

func loadOrCreateCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPath, keyPath := CAFile(dir), filepath.Join(dir, "ca.key")
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		ca, err := x509.ParseCertificate(pair.Certificate[0])
		if err == nil && now.Before(ca.NotAfter) {
			if key, ok := pair.PrivateKey.(*ecdsa.PrivateKey); ok {
				return ca, key, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "pooka CA on " + host, Organization: []string{"pooka"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePair(certPath, keyPath, der, key); err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(der)
	return ca, key, err
}

func issueLeaf(certPath, keyPath string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, domain string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "*." + domain, Organization: []string{"pooka"}},
		DNSNames:     Names(domain),
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writePair(certPath, keyPath, der, key); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func writePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	return nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(fmt.Sprintf("pki: random serial: %v", err))
	}
	return n
}
