package websocket

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"sync"
	"testing"

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

var tlsConfig *tls.Config

func loadTLSConfig(t *testing.T) {
	certFile := "../../localhost+2.pem"
	keyFile := "../../localhost+2-key.pem"
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	require.NoError(t, err)
	caBytes, err := ioutil.ReadFile("../../rootCA.pem")
	require.NoError(t, err)
	certPool := x509.NewCertPool()
	require.True(t, certPool.AppendCertsFromPEM(caBytes))
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      certPool,
	}
	tlsConfig = tlsCfg
}
