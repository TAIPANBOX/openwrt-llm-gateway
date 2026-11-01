package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	houseNames = []string{"llm.lan"}
	houseIPs   = []netip.Addr{netip.MustParseAddr("192.168.8.1"), netip.MustParseAddr("127.0.0.1")}
	t0         = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
)

func verifyLeaf(t *testing.T, c *certs, name string, now time.Time) error {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(c.CA)
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, CurrentTime: now})
	return err
}

func TestCertificatesAreCreatedOnceAndKeptPrivate(t *testing.T) {
	dir := t.TempDir()
	c1, err := ensureCerts(dir, houseNames, houseIPs, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"ca.key", "tls.key"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s has mode %v, want 0600", f, st.Mode().Perm())
		}
	}
	for _, name := range []string{"llm.lan", "192.168.8.1", "127.0.0.1"} {
		if err := verifyLeaf(t, c1, name, t0); err != nil {
			t.Fatalf("the gateway's certificate does not verify for %s: %v", name, err)
		}
	}
	c2, err := ensureCerts(dir, houseNames, houseIPs, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c1.Fingerprint != c2.Fingerprint || len(c1.Fingerprint) != 95 {
		t.Fatalf("CA fingerprint %q then %q: want one stable SHA-256 fingerprint", c1.Fingerprint, c2.Fingerprint)
	}
	if string(c1.Leaf.Certificate[0]) != string(c2.Leaf.Certificate[0]) {
		t.Fatal("a second start issued a new certificate although the first one was still good")
	}
	cfg := serverTLSConfig(c2)
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS floor is %x, want TLS 1.2", cfg.MinVersion)
	}
}

func TestHouseCACannotVouchForOtherNames(t *testing.T) {
	dir := t.TempDir()
	c, err := ensureCerts(dir, houseNames, houseIPs, t0)
	if err != nil {
		t.Fatal(err)
	}
	// Whoever takes the CA key off the router signs whatever they like with it.
	raw, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(raw)
	caKey, err := x509.ParseECPrivateKey(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(c.CA)
	forge := func(dns []string, ips []net.IP) error {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "forged"},
			NotBefore: t0.Add(-time.Hour), NotAfter: t0.Add(24 * time.Hour),
			DNSNames: dns, IPAddresses: ips, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, c.CA, &k.PublicKey, caKey)
		if err != nil {
			return err
		}
		leaf, _ := x509.ParseCertificate(der)
		name := ""
		if len(dns) > 0 {
			name = dns[0]
		} else {
			name = ips[0].String()
		}
		_, err = leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, CurrentTime: t0})
		return err
	}
	for _, name := range []string{"www.google.com", "bank.example", "lan", "evil.llm.lan.example"} {
		if forge([]string{name}, nil) == nil {
			t.Fatalf("a certificate for %s signed with the house CA verifies", name)
		}
	}
	for _, ip := range []string{"8.8.8.8", "203.0.113.5", "2001:db8::1"} {
		if forge(nil, []net.IP{net.ParseIP(ip)}) == nil {
			t.Fatalf("a certificate for %s signed with the house CA verifies", ip)
		}
	}
	// The constraint is what refuses them: the gateway's own names still verify.
	if err := forge([]string{"llm.lan"}, nil); err != nil {
		t.Fatalf("a certificate for llm.lan does not verify: %v", err)
	}
	if err := forge(nil, []net.IP{net.ParseIP("10.0.60.1")}); err != nil {
		t.Fatalf("a certificate for 10.0.60.1 does not verify: %v", err)
	}
	if !c.CA.PermittedDNSDomainsCritical {
		t.Fatal("the CA's name constraints are not critical, so a client that ignores them would trust anything")
	}
}

func TestLeafIsReissuedSilentlyUnderTheSameCA(t *testing.T) {
	dir := t.TempDir()
	c1, err := ensureCerts(dir, houseNames, houseIPs, t0)
	if err != nil {
		t.Fatal(err)
	}
	// Near the end of its life the certificate is replaced; the CA a client trusts is not.
	later := t0.Add(380 * 24 * time.Hour)
	c2, err := ensureCerts(dir, houseNames, houseIPs, later)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Fingerprint != c1.Fingerprint {
		t.Fatal("renewing the certificate changed the CA, so every client would have to trust it again")
	}
	if string(c2.Leaf.Certificate[0]) == string(c1.Leaf.Certificate[0]) {
		t.Fatal("the certificate was not renewed near its end")
	}
	if err := verifyLeaf(t, c2, "llm.lan", later.Add(300*24*time.Hour)); err != nil {
		t.Fatalf("the renewed certificate does not verify well into its life: %v", err)
	}
	// A new LAN address gets a new certificate, under the same CA.
	moved := []netip.Addr{netip.MustParseAddr("192.168.50.1"), netip.MustParseAddr("127.0.0.1")}
	c3, err := ensureCerts(dir, houseNames, moved, later)
	if err != nil {
		t.Fatal(err)
	}
	if c3.Fingerprint != c1.Fingerprint {
		t.Fatal("a new address changed the CA")
	}
	if err := verifyLeaf(t, c3, "192.168.50.1", later); err != nil {
		t.Fatalf("the certificate for the new address does not verify: %v", err)
	}
}
