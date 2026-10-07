// Package builder is this runner's side of BuildKit.
//
// For now it is one question: can the runner reach the daemon, with a client certificate, and
// is the daemon presenting a certificate this runner's CA vouches for. That is worth asking
// separately from "can it build", because the two fail for unrelated reasons and the panel
// should be able to say which one broke.
package builder

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"time"
)

// Address is the daemon, and the three files that make a client out of this process.
type Address struct {
	// Addr is host:port. Loopback in the pod this runs in, which is not the same as safe:
	// rootless BuildKit executes `RUN` steps in the daemon's own network namespace, so a
	// Dockerfile's script can reach this address too. That is what the client certificate is
	// for, and it is why this type has no way to be built without one.
	Addr string
	CA   string
	Cert string
	Key  string
}

// Ping opens a TLS connection with the client certificate and verifies the daemon against the
// CA, then says what it found.
//
// A handshake and nothing more, deliberately. The full check is a BuildKit RPC, and that
// belongs with the client that will do the building; until then the honest question is
// "does the credential work and is the far end who it claims to be", because a runner that
// reports "connected" when it only dialled a TCP port is the kind of green light that costs
// an afternoon.
func (a Address) Ping(ctx context.Context) (string, error) {
	certificate, err := tls.LoadX509KeyPair(a.Cert, a.Key)
	if err != nil {
		// Said this way round because it is the more useful of the two: a certificate the
		// runner cannot load is a volume that is not readable by uid 1000, and no amount of
		// reading about TLS will say so.
		return "", fmt.Errorf("load the client certificate (%s, %s): %w", a.Cert, a.Key, err)
	}

	authority, err := os.ReadFile(a.CA)
	if err != nil {
		return "", fmt.Errorf("read the CA (%s): %w", a.CA, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority) {
		return "", fmt.Errorf("the CA at %s is not a certificate this program can use", a.CA)
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			RootCAs:      pool,
			// No InsecureSkipVerify anywhere in this file, and there will not be one: a
			// connection to the builder that does not check who is on the other end is a
			// connection to whoever answered.
			MinVersion: tls.VersionTLS12,
		},
	}

	connection, err := dialer.DialContext(ctx, "tcp", a.Addr)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", a.Addr, err)
	}
	defer connection.Close()

	state := connection.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("%s completed a handshake without presenting a certificate", a.Addr)
	}
	peer := state.PeerCertificates[0]

	return fmt.Sprintf("tls%s to %s, certificate for %s",
		tlsVersionName(state.Version), a.Addr, peer.Subject.CommonName), nil
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	default:
		return fmt.Sprint(version)
	}
}
