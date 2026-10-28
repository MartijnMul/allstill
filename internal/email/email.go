// SPDX-License-Identifier: AGPL-3.0-only

// Package email sends Allstill's messages through the SMTP server an
// administrator configured, on the standard library alone (ADR 0001). The
// settings follow what other self-hosted services ask for, so a mail
// provider's usual instructions apply (intent/alerts/plan.md, step 4).
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Security is how the connection to the SMTP server is protected.
type Security string

const (
	// StartTLS connects in plain text and switches to TLS before anything
	// else; a server that does not offer it is refused, never used in the
	// clear. The usual choice, on port 587.
	StartTLS Security = "starttls"
	// TLS connects with TLS from the start, on port 465.
	TLS Security = "tls"
	// None sends in plain text, for a relay the operator trusts, such as
	// one in a container next to Allstill's.
	None Security = "none"
)

// Config is where messages go out. The password is kept nowhere but here.
type Config struct {
	Host     string
	Port     int
	Security Security
	Username string
	Password string
	From     string // as people see it, such as "Allstill <allstill@example.com>"
	// Hello is the name Allstill introduces itself with: the dashboard's
	// host name, as some relays refuse "localhost".
	Hello string
	// RootCAs, when set, are the only certificates trusted, for a relay with
	// a certificate of its own.
	RootCAs *x509.CertPool
	// Step, when set, is told each step that went well, to see how far
	// settings get; an error says what the server answered.
	Step func(string)

	// tls overrides the TLS settings, for tests.
	tls *tls.Config
}

// Timeout bounds one message, from connecting to the server's last answer.
const Timeout = 30 * time.Second

// Check says whether the settings can work, and fills in what follows from
// them: port 587, and TLS on ports 465 and 2465.
func (c *Config) Check() error {
	if c.Host == "" {
		return errors.New("no SMTP host is set")
	}
	if c.Security == "ssl" {
		c.Security = TLS // as many guides call it
	}
	if c.Port == 0 {
		c.Port = 587
		if c.Security == TLS {
			c.Port = 465
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("the SMTP port %d does not exist", c.Port)
	}
	if c.Security == "" {
		c.Security = StartTLS
		if c.Port == 465 || c.Port == 2465 {
			c.Security = TLS
		}
	}
	switch c.Security {
	case StartTLS:
		if c.Port == 465 {
			return errors.New("port 465 uses TLS from the start: set the security to tls")
		}
	case TLS:
	case None:
		if c.Username != "" && !local(c.Host) {
			return fmt.Errorf("a password is never sent without TLS, and %s is not this host", c.Host)
		}
	default:
		return fmt.Errorf("the SMTP security %q is starttls, tls or none", c.Security)
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("an SMTP user name needs a password, and a password a user name")
	}
	if c.From == "" {
		return errors.New("no From address is set, such as Allstill <allstill@example.com>")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("the From address %q cannot be read: %w", c.From, err)
	}
	if c.Hello == "" {
		c.Hello = "localhost"
	}
	return nil
}

// Plain says whether messages travel unencrypted beyond this host, which
// is worth a warning.
func (c *Config) Plain() bool {
	return c.Security == None && !local(c.Host)
}

// local is a host net/smtp sends a password to without TLS.
func local(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// ParseAddress reads one plain email address, as a person types it.
func ParseAddress(s string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil || a.Name != "" || len(a.Address) > 254 || strings.ContainsAny(a.Address, "\r\n") {
		return "", errors.New("this is not one email address, such as ada@example.com")
	}
	return a.Address, nil
}

// Message is one email to one address.
type Message struct {
	// ID makes its Message-ID, the same on every try, so a repeat after a
	// crash can be told apart.
	ID      string
	To      string
	Subject string
	Body    string // plain text; lines end in \n
}

// Outcome is what an attempt to send means.
type Outcome int

const (
	// Sent: the server took the message.
	Sent Outcome = iota
	// Retry: it may work later, such as when the server could not be
	// reached or answered with a 4xx code.
	Retry
	// Rejected: the server refused this recipient's mailbox for good: it
	// does not exist, or is disabled or full for good (enhanced codes 5.1.x
	// and 5.2.x).
	Rejected
	// Failed: it will not work as Allstill is set up, such as a wrong
	// password or a From address the server will not send for.
	Failed
)

// Send delivers m, and says what the outcome means. The error tells an
// administrator what the server said.
func (c *Config) Send(ctx context.Context, m Message, now time.Time) (Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return Failed, err
	}
	data := c.compose(m, from.Address, now)
	client, err := c.connect(ctx)
	if err != nil {
		return classify(err, false)
	}
	defer client.Close()
	if err := c.authenticate(client); err != nil {
		return classify(err, false)
	}
	if err := client.Mail(from.Address); err != nil {
		return classify(err, false)
	}
	c.step("sender " + from.Address + " accepted")
	if err := client.Rcpt(m.To); err != nil {
		return classify(err, true)
	}
	c.step("recipient " + m.To + " accepted")
	w, err := client.Data()
	if err != nil {
		return classify(err, false)
	}
	if _, err := w.Write(data); err != nil {
		return classify(err, false)
	}
	if err := w.Close(); err != nil {
		return classify(err, false)
	}
	c.step("message accepted for delivery")
	client.Quit()
	return Sent, nil
}

func (c *Config) step(s string) {
	if c.Step != nil {
		c.Step(s)
	}
}

func tlsVersion(client *smtp.Client) string {
	if state, ok := client.TLSConnectionState(); ok {
		return " (" + tls.VersionName(state.Version) + ")"
	}
	return ""
}

// Probe connects, starts TLS and signs in, then leaves without sending: at
// start, it shows whether the settings work.
func (c *Config) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	client, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := c.authenticate(client); err != nil {
		return err
	}
	return client.Quit()
}

func (c *Config) tlsConfig() *tls.Config {
	if c.tls != nil {
		return c.tls
	}
	return &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, RootCAs: c.RootCAs}
}

// connect opens the connection, protected as the settings say, and greets.
func (c *Config) connect(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err = dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	if c.Security == TLS {
		tc := tls.Client(conn, c.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, handshake(fmt.Errorf("starting TLS with %s: %w", addr, err))
		}
		conn = tc
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	c.step("connected to " + addr)
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("greeting %s: %w", addr, err)
	}
	if err := client.Hello(c.Hello); err != nil {
		client.Close()
		return nil, err
	}
	if c.Security == TLS {
		c.step("TLS from the start" + tlsVersion(client))
	}
	if c.Security == StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			client.Close()
			return nil, errNoStartTLS
		}
		if err := client.StartTLS(c.tlsConfig()); err != nil {
			client.Close()
			return nil, handshake(fmt.Errorf("starting TLS: %w", err))
		}
		c.step("started TLS" + tlsVersion(client))
	}
	return client, nil
}

var errNoStartTLS = errors.New("the server does not offer STARTTLS; Allstill will not send in plain text (set the security to tls for port 465)")

// settingsError is a failure that only a change of settings puts right.
type settingsError struct{ err error }

func (e settingsError) Error() string { return e.err.Error() }
func (e settingsError) Unwrap() error { return e.err }

func wrong(format string, a ...any) error { return settingsError{fmt.Errorf(format, a...)} }

// handshake reads a failed TLS handshake: a connection that timed out or
// dropped may work later; anything else (a certificate, a version or a
// cipher the two sides do not share, a server alert, TLS spoken to a port
// that starts in plain text) only works with other settings.
func handshake(err error) error {
	var ne net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, context.Canceled) || (errors.As(err, &ne) && ne.Timeout()) {
		return err
	}
	return settingsError{err}
}

// authenticate signs in with PLAIN, or LOGIN where the server offers only
// that, as some do.
func (c *Config) authenticate(client *smtp.Client) error {
	if c.Username == "" {
		return nil
	}
	ok, mechanisms := client.Extension("AUTH")
	if !ok {
		return wrong("the server does not take a user name and password")
	}
	offered := strings.Fields(strings.ToUpper(mechanisms))
	var auth smtp.Auth
	switch {
	case slices.Contains(offered, "PLAIN"):
		auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	case slices.Contains(offered, "LOGIN"):
		auth = &loginAuth{username: c.Username, password: c.Password}
	default:
		return wrong("the server signs in only with %s, which Allstill does not support", mechanisms)
	}
	if err := client.Auth(auth); err != nil {
		var te *textproto.Error
		if !errors.As(err, &te) {
			// Refused before asking the server, such as a password
			// without TLS.
			return wrong("signing in as %s: %w", c.Username, err)
		}
		return fmt.Errorf("signing in as %s: %w", c.Username, err)
	}
	c.step("signed in as " + c.Username)
	return nil
}

// loginAuth is the LOGIN mechanism: the user name and the password, each
// asked for in turn. Like PLAIN, it is only used over TLS or to this host.
type loginAuth struct {
	username, password string
	asked              int
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !local(server.Name) {
		return "", nil, errors.New("refusing to send a password without TLS")
	}
	return "LOGIN", nil, nil
}

// Next answers the server's two prompts in turn, whatever their wording.
func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.asked++
	switch a.asked {
	case 1:
		return []byte(a.username), nil
	case 2:
		return []byte(a.password), nil
	}
	return nil, errors.New("the server asked more than a user name and a password")
}

// classify reads an error: a 4xx answer or a lost connection may work
// later; a 5xx answer to the recipient rejects it; anything else is set up
// wrong.
func classify(err error, recipient bool) (Outcome, error) {
	var te *textproto.Error
	switch {
	case errors.As(err, &te) && te.Code >= 400 && te.Code < 500:
		return Retry, err
	case errors.As(err, &te) && recipient && mailbox(te.Msg):
		return Rejected, err
	case errors.As(err, &te):
		return Failed, err
	case errors.Is(err, errNoStartTLS), errors.As(err, new(settingsError)):
		return Failed, err
	}
	return Retry, err
}

// mailbox says whether a refusal is about the recipient's mailbox. Other
// refusals at the recipient, such as "Relay access denied" or a sender the
// login may not use, come from the installation's settings, as servers like
// Postfix give every refusal there.
//
// RFC 3463 gives 5.1.7 and 5.1.8 to the sender, and Postfix reports sender
// refusals at the recipient too ("Sender address rejected"): those are the
// settings' From address, not the recipient's mailbox.
func mailbox(msg string) bool {
	if strings.HasPrefix(msg, "5.1.7") || strings.HasPrefix(msg, "5.1.8") || strings.Contains(strings.ToLower(msg), "sender address") {
		return false
	}
	return strings.HasPrefix(msg, "5.1.") || strings.HasPrefix(msg, "5.2.")
}

// compose writes the message: plain text in UTF-8, marked as sent by a
// machine so autoresponders keep quiet.
func (c *Config) compose(m Message, from string, now time.Time) []byte {
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	if f, err := mail.ParseAddress(c.From); err == nil {
		header("From", f.String()) // a name beyond ASCII is encoded
	} else {
		header("From", c.From)
	}
	header("To", m.To)
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	header("Date", now.Format(time.RFC1123Z))
	id := m.ID
	if id == "" {
		id = rand.Text()
	}
	header("Message-ID", "<"+id+"@"+domainOf(from)+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	header("X-Auto-Response-Suppress", "All")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n")))
	qp.Close()
	return b.Bytes()
}

// oneLine keeps a header to one line: a check's name could hold a line
// break, which would start a header of its own.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func domainOf(address string) string {
	if i := strings.LastIndexByte(address, '@'); i >= 0 {
		return address[i+1:]
	}
	return "localhost"
}
