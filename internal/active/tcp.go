// SPDX-License-Identifier: AGPL-3.0-only

package active

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/outbound"
)

// TCP makes one attempt of a TCP check and judges it: the port must accept a
// connection and, with TLS, finish a handshake with a certificate that holds.
// Nothing else is sent, and the connection is closed at once. Measured holds
// how long it took (ms) and the days the certificate has left (cert_days).
func (r *Runner) TCP(ctx context.Context, t TCP) Result {
	res := Result{Started: time.Now(), Measured: map[string]float64{}}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dial := r.Dial
	if dial == nil {
		dial = outbound.Dialer(timeout, r.Allow).DialContext
	}
	failed := func(err error) Result {
		res.Ended = time.Now()
		res.Measured["ms"] = float64(res.Ended.Sub(res.Started).Milliseconds())
		res.Local = errors.Is(err, outbound.ErrRefused)
		reason, unsure := explain(err, timeout)
		if reason == requestFailed {
			reason = "the connection failed" // it sends no request
		}
		return res.fail(reason, unsure)
	}
	conn, err := dial(ctx, "tcp", t.Address())
	if err != nil {
		return failed(err)
	}
	defer conn.Close()
	if !t.TLS {
		res.Ended = time.Now()
		res.Measured["ms"] = float64(res.Ended.Sub(res.Started).Milliseconds())
		res.Outcome = check.Passed
		return res
	}
	certs := &certificates{}
	conf := certs.config(r.trust(t.CA), t.NoVerify)
	conf.ServerName = t.Host
	tc := tls.Client(conn, conf)
	if err := tc.HandshakeContext(ctx); err != nil {
		return failed(err)
	}
	res.Ended = time.Now()
	res.Measured["ms"] = float64(res.Ended.Sub(res.Started).Milliseconds())
	cert := res.measureCertificate(certs)
	tc.Close() // says goodbye; the deferred close is for the attempts that do not get here
	return res.judgeCertificate(t.WarnDays, t.FailDays, cert)
}
