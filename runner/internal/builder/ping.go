// Checking that the builder is there, and who it is.
//
// Deliberately less than a BuildKit RPC. The full check belongs to the client that will do the
// building, and a runner that reported "connected" after dialling a TCP port would be right in
// exactly the case where the certificate was wrong — which is the case worth catching.

package builder

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"
)

// Ping opens a TLS connection with the credential in hand and checks what the far end says it
// is.
//
// A handshake and nothing more, said plainly in the result it returns: "tls1.3 to
// 127.0.0.1:1234, the daemon's certificate is for buildkit.buildkit.svc". A word like
// "connected" on its own is a claim the code has not earned.
func (c *Credentials) Ping(ctx context.Context, address, serverName string) (string, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    c.TLSConfig(serverName),
	}

	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", address, err)
	}
	defer connection.Close()

	state := connection.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("%s completed a handshake without presenting a certificate", address)
	}

	return fmt.Sprintf("tls%s to %s, the daemon's certificate is for %s",
		tlsVersionName(state.Version), address, state.PeerCertificates[0].Subject.CommonName), nil
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
