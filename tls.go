package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The router keeps a small CA of its own. A client trusts it once; the gateway's certificate
// under it is renewed and re-issued for new addresses without the client noticing. The CA is
// name-constrained to the gateway's names and the private address ranges, so its key, if it
// ever leaves the router, cannot vouch for any other site to a client that trusts it.

type certs struct {
	CA          *x509.Certificate
	CAPEM       []byte
	Fingerprint string // SHA-256 of the CA, as a client's trust dialog shows it
	Leaf        tls.Certificate
}

const (
	caLife       = 20 * 365 * 24 * time.Hour
	leafLife     = 397 * 24 * time.Hour
	leafRenewal  = 30 * 24 * time.Hour
	caCertFile   = "ca.crt"
	caKeyFile    = "ca.key"
	leafCertFile = "tls.crt"
	leafKeyFile  = "tls.key"
)

var housePrefixes = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "fc00::/7"}

// ensureCerts is prepare's, and the daemon never reaches it (invariant 14).
func ensureCerts(dir string, names []string, ips []netip.Addr, now time.Time) (*certs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ca, caKey, err := loadCA(dir)
	if err != nil {
		return nil, err
	}
	if !caCovers(ca, names, now) {
		if ca, caKey, err = newCA(dir, names, now); err != nil {
			return nil, err
		}
	}
	leaf, leafKey, err := loadPair(dir, leafCertFile, leafKeyFile)
	if err != nil || !leafGood(leaf, ca, names, ips, now) {
		if leaf, leafKey, err = newLeaf(dir, ca, caKey, names, ips, now); err != nil {
			return nil, err
		}
	}
	return &certs{
		CA:          ca,
		CAPEM:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}),
		Fingerprint: fingerprint(ca.Raw),
		Leaf:        tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey, Leaf: leaf},
	}, nil
}

// loadCA reads the CA, and returns nothing at all when there is none yet. A CA that is there and
// cannot be used stops the caller: every device that trusts it would have to trust a new one, so
// a new one is made only into an empty place, or by someone who has emptied it.
func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	ca, key, err := loadPair(dir, caCertFile, caKeyFile)
	if err == nil {
		return ca, key, nil
	}
	_, certErr := os.Lstat(filepath.Join(dir, caCertFile))
	_, keyErr := os.Lstat(filepath.Join(dir, caKeyFile))
	if errors.Is(certErr, fs.ErrNotExist) && errors.Is(keyErr, fs.ErrNotExist) {
		return nil, nil, nil
	}
	return nil, nil, fmt.Errorf("the CA in %s cannot be used (%w): restore it, or remove %s and %s to make a new one that every device must trust again", dir, err, caCertFile, caKeyFile)
}

// loadLeaf reads the gateway's certificate and key as prepare left them. It writes nothing and
// opens neither of the CA's files: this is all of TLS the daemon has.
func loadLeaf(dir string) (tls.Certificate, error) {
	cert, key, err := loadPair(dir, leafCertFile, leafKeyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key, Leaf: cert}, nil
}

// fingerprint is the SHA-256 of a certificate as trust dialogs and openssl print it.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(sum))
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

func serverTLSConfig(leaf tls.Certificate) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{leaf}}
}

func caCovers(ca *x509.Certificate, names []string, now time.Time) bool {
	if ca == nil || !ca.IsCA || now.After(ca.NotAfter.Add(-leafLife)) || !ca.PermittedDNSDomainsCritical {
		return false
	}
	for _, n := range names {
		if !slices.Contains(ca.PermittedDNSDomains, n) {
			return false
		}
	}
	return true
}

// leafCurrent says whether a certificate is in force and not yet near its end.
func leafCurrent(leaf *x509.Certificate, now time.Time) bool {
	return leaf != nil && !now.Before(leaf.NotBefore) && leaf.NotAfter.Sub(now) >= leafRenewal
}

func leafGood(leaf, ca *x509.Certificate, names []string, ips []netip.Addr, now time.Time) bool {
	if !leafCurrent(leaf, now) || leaf.CheckSignatureFrom(ca) != nil {
		return false
	}
	have := slices.Clone(leaf.DNSNames)
	want := slices.Clone(names)
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) || len(leaf.IPAddresses) != len(ips) {
		return false
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(leaf.IPAddresses, func(x net.IP) bool { return x.Equal(net.IP(ip.AsSlice())) }) {
			return false
		}
	}
	return true
}

func newCA(dir string, names []string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	var ranges []*net.IPNet
	for _, p := range housePrefixes {
		_, n, _ := net.ParseCIDR(p)
		ranges = append(ranges, n)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "llm-gateway house CA " + serial.Text(16)[:8]},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caLife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         slices.Clone(names),
		PermittedIPRanges:           ranges,
	}
	return writePair(dir, caCertFile, caKeyFile, tmpl, tmpl, key, key)
}

func newLeaf(dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, names []string, ips []netip.Addr, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if caKey == nil {
		return nil, nil, errors.New("the CA key is missing")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     slices.Clone(names),
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(ip.AsSlice()))
	}
	return writePair(dir, leafCertFile, leafKeyFile, tmpl, ca, key, caKey)
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func writePair(dir, certFile, keyFile string, tmpl, parent *x509.Certificate, key, signer *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func loadPair(dir, certFile, keyFile string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, err := os.ReadFile(filepath.Join(dir, certFile))
	if err != nil {
		return nil, nil, err
	}
	kb, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, nil, err
	}
	cp, _ := pem.Decode(cb)
	kp, _ := pem.Decode(kb)
	if cp == nil || kp == nil {
		return nil, nil, fmt.Errorf("%s or %s is not PEM", certFile, keyFile)
	}
	cert, err := x509.ParseCertificate(cp.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kp.Bytes)
	if err != nil {
		return nil, nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, fmt.Errorf("%s does not belong to %s", keyFile, certFile)
	}
	return cert, key, nil
}

func readCert(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, fmt.Errorf("%s is not PEM", path)
	}
	return x509.ParseCertificate(blk.Bytes)
}

// writeFileAtomic writes a sibling and renames it over path, so a reader never sees half a key
// and a key file is never, even briefly, readable by others.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return writeFileOwned(path, data, mode, nil)
}

// writeFileOwned is writeFileAtomic for a file with an owner of its own: own, when given, runs on
// the sibling before it takes the name, so the file never stands there with the wrong owner.
func writeFileOwned(path string, data []byte, mode os.FileMode, own func(tmp string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if own != nil {
		if err := own(tmp.Name()); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}
