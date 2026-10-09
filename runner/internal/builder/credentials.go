// The credential this runner holds to the builder, and the end of its life on disk.
//
// The key is read once at startup and the files are deleted immediately afterwards. That is not
// tidiness: job scripts run inside this container, so anything left at /certs is readable by
// somebody else's shell script, and a client certificate is exactly the thing that must not be
// readable by a Dockerfile being built. BuildKit's own WithCredentials takes a path and reads
// it on every dial, so the file would have to stay — hence the transport credentials being
// assembled here instead, and the key living in this process's memory where a child cannot
// reach it without privileges it does not have.
package builder

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Credentials is a client certificate, its key and the authority that vouches for the builder,
// all in memory.
type Credentials struct {
	certificate tls.Certificate
	authority   *x509.CertPool
}

// LoadCredentials reads the three files and then removes them.
//
// Removal is the point, and it is reported separately from loading so that a failure to delete
// is visible: a key still on disk after startup means a job script could read it, and that is
// worth saying out loud rather than assuming.
func LoadCredentials(certificatePath, keyPath, caPath string) (*Credentials, error) {
	certificate, err := tls.LoadX509KeyPair(certificatePath, keyPath)
	if err != nil {
		// Named this way round because it is the more useful of the two failures: a
		// certificate that cannot be loaded is almost always a volume uid 1000 cannot read,
		// and no amount of reading about TLS will say so.
		return nil, fmt.Errorf("load the client certificate (%s, %s): %w", certificatePath, keyPath, err)
	}

	raw, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read the CA (%s): %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("the CA at %s is not a certificate this program can use", caPath)
	}

	for _, path := range []string{keyPath, certificatePath, caPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			// Not fatal — the key is in memory either way — but the operator should know,
			// because a key left on disk is a key a job script can read.
			fmt.Fprintf(os.Stderr, "runner: warning: could not remove %s: %v\n", path, err)
		}
	}

	return &Credentials{certificate: certificate, authority: pool}, nil
}

// TLSConfig is what to dial the builder with.
//
// ServerName is set explicitly rather than left to the dialler, because a certificate is issued
// for names and the one here is issued for the builder's service name and for 127.0.0.1. Naming
// the right one is a change when the daemon moves; guessing produces a certificate error,
// which is a worse afternoon than a name mismatch would be.
//
// No InsecureSkipVerify anywhere in this file, and there will not be one. A connection to the
// builder that does not check who is on the other end is a connection to whoever answered.
func (c *Credentials) TLSConfig(serverName string) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{c.certificate},
		RootCAs:      c.authority,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}
}

// PeerName is the authority's own name, for saying in a heartbeat what was on the other end.
func (c *Credentials) PeerName() string {
	if len(c.certificate.Certificate) == 0 {
		return ""
	}
	leaf, err := x509.ParseCertificate(c.certificate.Certificate[0])
	if err != nil {
		return ""
	}
	return leaf.Subject.CommonName
}
