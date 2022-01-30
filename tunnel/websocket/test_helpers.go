package websocket

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const MaxUint = ^uint(0)
const MinUint = 0
const MaxInt = int(MaxUint >> 1)
const MinInt = -MaxInt - 1

type ConcurrencyCounter struct {
	count int
	min   int
	max   int
	mutex sync.Mutex
}

func NewCounter() *ConcurrencyCounter {
	cc := &ConcurrencyCounter{}
	cc.Init(0)
	return cc
}

func (c *ConcurrencyCounter) Init(val int) {
	c.mutex.Lock()
	c.count = val
	c.min = MaxInt
	c.max = MinInt
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Set(val int) {
	c.mutex.Lock()
	c.count = val
	c.update()
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Inc() {
	c.mutex.Lock()
	c.count++
	c.update()
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Dec() {
	c.mutex.Lock()
	c.count--
	c.update()
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Count() int {
	return c.count
}

func (c *ConcurrencyCounter) Max() int {
	return c.max
}

func (c *ConcurrencyCounter) Min() int {
	return c.min
}

func (c *ConcurrencyCounter) update() {
	if c.count > c.max {
		c.max = c.count
	}
	if c.count < c.min {
		c.min = c.count
	}
}

func generateTLSConfigs(t *testing.T) (serverCfg tls.Config, clientCfg tls.Config) {
	dnsNames := []string{"localhost"}
	ipAddrs := []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("::")}
	caCert, caKey, err := generateSelfSignedCA()
	require.NoError(t, err)
	cert, err := generateTLSCert(caCert, caKey, big.NewInt(1), dnsNames, ipAddrs)
	require.NoError(t, err)
	serverCfg.Certificates = append(serverCfg.Certificates, cert)
	clientCfg.RootCAs = x509.NewCertPool()
	clientCfg.RootCAs.AddCert(caCert)
	return
}

func generateSelfSignedCA() (*x509.Certificate, crypto.PrivateKey, error) {
	pubKey, prvKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		NotAfter:              time.Now().Add(12 * time.Hour),
		NotBefore:             time.Now(),
		PublicKeyAlgorithm:    x509.Ed25519,
		PublicKey:             pubKey,
		SerialNumber:          big.NewInt(0),
		Subject: pkix.Name{
			CommonName: "Test CA",
		},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pubKey, prvKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, prvKey, nil
}

func generateTLSCert(caCert *x509.Certificate, caKey crypto.PrivateKey, serial *big.Int, dnsNames []string, ipAddrs []net.IP) (cert tls.Certificate, err error) {
	pubKey, prvKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	tmpl := &x509.Certificate{
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:           dnsNames,
		IPAddresses:        ipAddrs,
		KeyUsage:           x509.KeyUsageDigitalSignature,
		NotBefore:          time.Now(),
		NotAfter:           time.Now().Add(12 * time.Hour),
		PublicKeyAlgorithm: x509.Ed25519,
		PublicKey:          pubKey,
		SerialNumber:       serial,
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pubKey, caKey)
	if err != nil {
		return
	}
	leaf, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return
	}
	cert.Certificate = append(cert.Certificate, certBytes)
	cert.Leaf = leaf
	cert.PrivateKey = prvKey
	return
}
