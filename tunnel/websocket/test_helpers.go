package websocket

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/banzaicloud/kurun/tunnel/pkg/tlstools"
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
	caCert, caKey, err := tlstools.GenerateSelfSignedCA()
	require.NoError(t, err)
	cert, err := tlstools.GenerateTLSCert(caCert, caKey, big.NewInt(1), dnsNames, ipAddrs)
	require.NoError(t, err)
	serverCfg.Certificates = append(serverCfg.Certificates, cert)
	clientCfg.RootCAs = x509.NewCertPool()
	clientCfg.RootCAs.AddCert(caCert)
	return
}

func compareRequests(t *testing.T, orig, recv *http.Request) {
	t.Helper()
	require.NotNil(t, orig)
	require.NotNil(t, recv)
	require.Equal(t, orig.Method, recv.Method)
	require.Equal(t, orig.URL.Path, recv.URL.Path)
	require.Equal(t, orig.Proto, recv.Proto)
	require.Equal(t, orig.ProtoMajor, recv.ProtoMajor)
	require.Equal(t, orig.ProtoMinor, recv.ProtoMinor)
	requireHeaderSubset(t, orig.Header, recv.Header)
	requireReaderContentEqual(t, getRequestBody(t, orig), getRequestBody(t, recv))
}

func compareResponses(t *testing.T, orig, recv *http.Response) {
	t.Helper()
	require.NotNil(t, orig)
	require.NotNil(t, recv)
	if orig.Status != "" {
		require.Equal(t, orig.Status, recv.Status)
	}
	require.Equal(t, orig.StatusCode, recv.StatusCode)
	require.Equal(t, orig.Proto, recv.Proto)
	require.Equal(t, orig.ProtoMajor, recv.ProtoMajor)
	require.Equal(t, orig.ProtoMinor, recv.ProtoMinor)
	requireHeaderSubset(t, orig.Header, recv.Header)
	requireReaderContentEqual(t, orig.Body, recv.Body)
}

func requireHeaderSubset(t *testing.T, expected, actual http.Header, msgAndArgs ...interface{}) {
	t.Helper()
	for k, vs := range expected {
		require.Contains(t, actual, k, msgAndArgs...)
		require.Subset(t, actual.Values(k), vs, msgAndArgs...)
	}
}

func requireReaderContentEqual(t *testing.T, expected, actual io.Reader, msgAndArgs ...interface{}) {
	t.Helper()
	if expected == nil {
		if actual != nil {
			bs, err := io.ReadAll(actual)
			require.NoError(t, err)
			require.Len(t, bs, 0, msgAndArgs...)
		}
	} else {
		if seeker, ok := expected.(io.Seeker); ok {
			seeker.Seek(0, io.SeekStart)
		}
		require.NotNil(t, actual, msgAndArgs...)
		expectedBytes, err := io.ReadAll(expected)
		require.NoError(t, err)
		actualBytes, err := io.ReadAll(actual)
		require.NoError(t, err)
		require.Equal(t, expectedBytes, actualBytes, msgAndArgs...)
	}
}

func getRequestBody(t *testing.T, r *http.Request) io.ReadCloser {
	t.Helper()
	if r.GetBody != nil {
		b, err := r.GetBody()
		require.NoError(t, err)
		return b
	}
	return r.Body
}

func NopReadSeekCloser(rs io.ReadSeeker) nopReadSeekCloser {
	return nopReadSeekCloser{
		ReadSeeker: rs,
	}
}

type nopReadSeekCloser struct {
	io.ReadSeeker
}

func (nopReadSeekCloser) Close() error {
	return nil
}
