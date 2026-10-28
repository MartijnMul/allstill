// SPDX-License-Identifier: AGPL-3.0-only

package email

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// server is a fake SMTP server: it offers what the test asks, answers RCPT
// with rcpt, and keeps what it was sent.
type server struct {
	t          *testing.T
	ln         net.Listener
	cert       tls.Certificate
	pool       *x509.CertPool
	implicit   bool   // TLS from the start
	maxVersion uint16 // the newest TLS it speaks, when set
	starttls   bool   // offers STARTTLS
	auth       string // the AUTH mechanisms it offers, "" for none
	rcpt       string // the answer to RCPT
	mu         sync.Mutex
	user       string
	pass       string
	from       string
	to         string
	data       string
	tls        bool // the message came over TLS
}

// certificate makes a certificate for 127.0.0.1 for this test run only.
func certificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func newServer(t *testing.T, s *server) *server {
	s.t = t
	s.cert, s.pool = certificate(t)
	if s.rcpt == "" {
		s.rcpt = "250 ok"
	}
	var err error
	if s.implicit {
		s.ln, err = tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{s.cert}, MaxVersion: s.maxVersion})
	} else {
		s.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.ln.Close() })
	go func() {
		for {
			conn, err := s.ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, s.implicit, true)
		}
	}()
	return s
}

// seen is who signed in, and whether the message came over TLS.
func (s *server) seen() (user, pass string, secure bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.user, s.pass, s.tls
}

func (s *server) config() *Config {
	_, port, _ := net.SplitHostPort(s.ln.Addr().String())
	c := &Config{Host: "127.0.0.1", From: "Allstill <allstill@example.com>", RootCAs: s.pool}
	c.Port = atoi(port)
	if s.implicit {
		c.Security = TLS
	}
	return c
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *server) serve(conn net.Conn, secure, greet bool) {
	defer conn.Close()
	r, w := bufio.NewReader(conn), conn
	say := func(line string) { io.WriteString(w, line+"\r\n") }
	if greet {
		say("220 test ready")
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(cmd + " x")[0])
		switch verb {
		case "EHLO":
			lines := []string{"250-test"}
			if s.starttls && !secure {
				lines = append(lines, "250-STARTTLS")
			}
			if s.auth != "" {
				lines = append(lines, "250-AUTH "+s.auth)
			}
			lines = append(lines, "250 8BITMIME")
			for _, l := range lines {
				say(l)
			}
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if tc.Handshake() != nil {
				return
			}
			s.serve(tc, true, false)
			return
		case "AUTH":
			parts := strings.Fields(cmd)
			switch strings.ToUpper(parts[1]) {
			case "PLAIN":
				b, _ := base64.StdEncoding.DecodeString(parts[2])
				f := strings.Split(string(b), "\x00")
				s.mu.Lock()
				s.user, s.pass = f[1], f[2]
				s.mu.Unlock()
			case "LOGIN":
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := r.ReadString('\n')
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := r.ReadString('\n')
				ub, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(u))
				pb, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
				s.mu.Lock()
				s.user, s.pass = string(ub), string(pb)
				s.mu.Unlock()
			}
			if s.pass != "right" {
				say("535 5.7.8 authentication failed")
				continue
			}
			say("235 ok")
		case "MAIL":
			s.mu.Lock()
			s.from = cmd
			s.mu.Unlock()
			say("250 ok")
		case "RCPT":
			s.mu.Lock()
			s.to = cmd
			s.mu.Unlock()
			say(s.rcpt)
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data, s.tls = b.String(), secure
			s.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestSendWithStartTLSAndPlain(t *testing.T) {
	s := newServer(t, &server{starttls: true, auth: "PLAIN LOGIN"})
	c := s.config()
	c.From = "Allstill Café <allstill@example.com>"
	c.Username, c.Password = "ada@example.com", "right"
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	var steps []string
	c.Step = func(s string) { steps = append(steps, s) }
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	out, err := c.Send(t.Context(), Message{ID: "dlv_1", To: "ada@example.com", Subject: "backup is down\r\nBcc: x@example.net", Body: "backup is down\nSince 09:00 · café\n"}, now)
	if out != Sent || err != nil {
		t.Fatalf("%v %v", out, err)
	}
	if strings.Join(steps, "|") != "connected to "+net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))+"|started TLS (TLS 1.3)|signed in as ada@example.com|sender allstill@example.com accepted|recipient ada@example.com accepted|message accepted for delivery" {
		t.Errorf("steps %q", steps)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tls || s.user != "ada@example.com" || s.pass != "right" || s.from != "MAIL FROM:<allstill@example.com> BODY=8BITMIME" && s.from != "MAIL FROM:<allstill@example.com>" || s.to != "RCPT TO:<ada@example.com>" {
		t.Fatalf("tls %v, user %q, from %q, to %q", s.tls, s.user, s.from, s.to)
	}
	msg, err := mail.ReadMessage(strings.NewReader(s.data))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Bcc") != "" || msg.Header.Get("Subject") != "backup is down Bcc: x@example.net" {
		t.Errorf("a line break in the subject made a header: %v", msg.Header)
	}
	if from, err := msg.Header.AddressList("From"); err != nil || from[0].Name != "Allstill Café" || strings.Contains(s.data, "Café <") {
		t.Errorf("From %v %v: a name beyond ASCII must be encoded", from, err)
	}
	if msg.Header.Get("Auto-Submitted") != "auto-generated" || msg.Header.Get("Message-ID") != "<dlv_1@example.com>" {
		t.Errorf("headers %v", msg.Header)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if string(body) != "backup is down\r\nSince 09:00 · café\r\n" {
		t.Errorf("body %q", body)
	}
}

func TestSendWithImplicitTLSAndLogin(t *testing.T) {
	s := newServer(t, &server{implicit: true, auth: "LOGIN"})
	c := s.config()
	c.Username, c.Password = "ada", "right"
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if out, err := c.Send(t.Context(), Message{To: "ada@example.com", Subject: "test", Body: "test\n"}, time.Now()); out != Sent || err != nil {
		t.Fatalf("%v %v", out, err)
	}
	if user, _, secure := s.seen(); user != "ada" || !secure {
		t.Errorf("user %q, tls %v", user, secure)
	}
}

func TestWhatFailuresMean(t *testing.T) {
	msg := Message{To: "ada@example.com", Subject: "test", Body: "test\n"}
	t.Run("no STARTTLS is never plain text", func(t *testing.T) {
		s := newServer(t, &server{auth: "PLAIN"})
		c := s.config()
		c.Username, c.Password = "ada", "right"
		c.Check()
		out, err := c.Send(t.Context(), msg, time.Now())
		if _, pass, _ := s.seen(); out != Failed || err == nil || pass != "" {
			t.Errorf("%v %v, password seen: %q", out, err, pass)
		}
	})
	t.Run("a wrong password", func(t *testing.T) {
		s := newServer(t, &server{starttls: true, auth: "PLAIN"})
		c := s.config()
		c.Username, c.Password = "ada", "wrong"
		c.Check()
		if out, err := c.Send(t.Context(), msg, time.Now()); out != Failed || !strings.Contains(err.Error(), "535") {
			t.Errorf("%v %v", out, err)
		}
	})
	t.Run("a recipient refused for good", func(t *testing.T) {
		s := newServer(t, &server{starttls: true, rcpt: "550 5.1.1 no such user"})
		c := s.config()
		c.Check()
		if out, err := c.Send(t.Context(), msg, time.Now()); out != Rejected || err == nil {
			t.Errorf("%v %v", out, err)
		}
	})
	t.Run("a refusal at the recipient that comes from the settings", func(t *testing.T) {
		s := newServer(t, &server{starttls: true, rcpt: "554 5.7.1 <ada@example.com>: Relay access denied"})
		c := s.config()
		c.Check()
		if out, err := c.Send(t.Context(), msg, time.Now()); out != Failed || err == nil {
			t.Errorf("%v %v", out, err)
		}
	})
	t.Run("a sender refused at the recipient", func(t *testing.T) {
		for _, answer := range []string{
			"550 5.1.8 <allstill@example.com>: Sender address rejected: Domain not found",
			"550 5.1.0 <allstill@example.com>: Sender address rejected: User unknown",
		} {
			s := newServer(t, &server{starttls: true, rcpt: answer})
			c := s.config()
			c.Check()
			if out, _ := c.Send(t.Context(), msg, time.Now()); out != Failed {
				t.Errorf("%q: %v", answer, out)
			}
		}
	})
	t.Run("a recipient refused for now", func(t *testing.T) {
		s := newServer(t, &server{starttls: true, rcpt: "451 4.7.1 try again later"})
		c := s.config()
		c.Check()
		if out, _ := c.Send(t.Context(), msg, time.Now()); out != Retry {
			t.Errorf("%v", out)
		}
	})
	t.Run("an untrusted certificate", func(t *testing.T) {
		s := newServer(t, &server{starttls: true})
		c := s.config()
		c.RootCAs = x509.NewCertPool()
		c.Check()
		if out, _ := c.Send(t.Context(), msg, time.Now()); out != Failed {
			t.Errorf("%v", out)
		}
	})
	t.Run("TLS to a port that starts in plain text", func(t *testing.T) {
		s := newServer(t, &server{starttls: true})
		c := s.config()
		c.Security = TLS
		c.Check()
		if out, err := c.Send(t.Context(), msg, time.Now()); out != Failed {
			t.Errorf("%v %v", out, err)
		}
	})
	t.Run("a TLS version the server refuses", func(t *testing.T) {
		s := newServer(t, &server{implicit: true, maxVersion: tls.VersionTLS11})
		c := s.config()
		c.Check()
		if out, err := c.Send(t.Context(), msg, time.Now()); out != Failed {
			t.Errorf("%v %v", out, err)
		}
	})
	t.Run("a password where the server takes none", func(t *testing.T) {
		s := newServer(t, &server{starttls: true})
		c := s.config()
		c.Username, c.Password = "ada", "right"
		c.Check()
		if out, err := c.Send(t.Context(), msg, time.Now()); out != Failed {
			t.Errorf("%v %v", out, err)
		}
	})
	t.Run("nothing listening", func(t *testing.T) {
		c := &Config{Host: "127.0.0.1", Port: 1, From: "allstill@example.com"}
		c.Check()
		if out, _ := c.Send(t.Context(), msg, time.Now()); out != Retry {
			t.Errorf("%v", out)
		}
	})
}

func TestProbe(t *testing.T) {
	s := newServer(t, &server{starttls: true, auth: "PLAIN"})
	c := s.config()
	c.Username, c.Password = "ada", "right"
	c.Check()
	err := c.Probe(t.Context())
	s.mu.Lock()
	pass, data := s.pass, s.data
	s.mu.Unlock()
	if err != nil || pass != "right" || data != "" {
		t.Errorf("%v, password %q, sent %q", err, pass, data)
	}
	c.Password = "wrong"
	if err := c.Probe(t.Context()); err == nil {
		t.Error("a wrong password passed")
	}
}

func TestCheck(t *testing.T) {
	c := Config{Host: "smtp.example.com", From: "Allstill <allstill@example.com>"}
	if err := c.Check(); err != nil || c.Port != 587 || c.Security != StartTLS {
		t.Errorf("defaults: %v %+v", err, c)
	}
	c = Config{Host: "smtp.example.com", Port: 465, From: "allstill@example.com"}
	if err := c.Check(); err != nil || c.Security != TLS {
		t.Errorf("port 465: %v %+v", err, c)
	}
	for _, bad := range []Config{
		{From: "allstill@example.com"},
		{Host: "smtp.example.com"},
		{Host: "smtp.example.com", From: "not an address"},
		{Host: "smtp.example.com", From: "a@example.com", Security: None, Username: "ada", Password: "secret"},
		{Host: "smtp.example.com", From: "a@example.com", Security: "smtps"},
		{Host: "smtp.example.com", From: "a@example.com", Username: "ada"},
	} {
		if err := bad.Check(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	local := Config{Host: "localhost", Port: 25, Security: None, From: "a@example.com"}
	if err := local.Check(); err != nil || local.Plain() {
		t.Errorf("a relay on this host: %v, plain %v", err, local.Plain())
	}
	// A relay in another container, without a password: allowed, with a
	// warning.
	relay := Config{Host: "postfix", Port: 25, Security: None, From: "a@example.com"}
	if err := relay.Check(); err != nil || !relay.Plain() {
		t.Errorf("a relay next to Allstill: %v, plain %v", err, relay.Plain())
	}
	if c := (Config{Host: "smtp.example.com", Port: 465, Security: StartTLS, From: "a@example.com"}); c.Check() == nil {
		t.Error("starttls on port 465 was taken")
	}
	if c := (Config{Host: "smtp.example.com", Port: 2465, From: "a@example.com"}); c.Check() != nil || c.Security != TLS {
		t.Errorf("port 2465: %+v", c)
	}
	if c := (Config{Host: "smtp.example.com", Security: "ssl", From: "a@example.com"}); c.Check() != nil || c.Security != TLS || c.Port != 465 {
		t.Errorf("ssl without a port: %+v", c)
	}
}

func TestParseAddress(t *testing.T) {
	if a, err := ParseAddress(" ada@example.com "); err != nil || a != "ada@example.com" {
		t.Errorf("%q %v", a, err)
	}
	for _, bad := range []string{"", "ada", "Ada <ada@example.com>", "ada@example.com, bob@example.com", "ada@example.com\r\nBcc: x@example.net"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
