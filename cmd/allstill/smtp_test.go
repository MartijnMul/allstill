// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// relay is a mail relay next to Allstill, without TLS or a password, as a
// Postfix container often is; it keeps the commands it was sent.
func relay(t *testing.T) (addr string, commands func() string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var got bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		io.WriteString(conn, "220 relay\r\n")
		data := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if data {
				if line == ".\r\n" {
					data = false
					io.WriteString(conn, "250 queued\r\n")
				}
				continue
			}
			got.WriteString(strings.Fields(line + " x")[0] + " ")
			switch {
			case strings.HasPrefix(line, "EHLO"):
				io.WriteString(conn, "250 relay\r\n")
			case strings.HasPrefix(line, "DATA"):
				data = true
				io.WriteString(conn, "354 go on\r\n")
			case strings.HasPrefix(line, "QUIT"):
				io.WriteString(conn, "221 bye\r\n")
				return
			default:
				io.WriteString(conn, "250 ok\r\n")
			}
		}
	}()
	return ln.Addr().String(), func() string { <-done; return strings.TrimSpace(got.String()) }
}

// clearSMTP keeps settings from the shell running the tests out of them.
func clearSMTP(t *testing.T) {
	for _, name := range []string{"HOST", "PORT", "SECURITY", "USERNAME", "PASSWORD", "PASSWORD_FILE", "FROM", "CA_FILE"} {
		t.Setenv("ALLSTILL_SMTP_"+name, "")
	}
}

func TestTestEmail(t *testing.T) {
	clearSMTP(t)
	addr, commands := relay(t)
	host, port, _ := net.SplitHostPort(addr)
	t.Setenv("ALLSTILL_SMTP_HOST", host)
	t.Setenv("ALLSTILL_SMTP_PORT", port)
	t.Setenv("ALLSTILL_SMTP_SECURITY", "none")
	t.Setenv("ALLSTILL_SMTP_FROM", "Allstill <allstill@example.com>")
	var stdout, stderr bytes.Buffer
	if err := testEmail(t.Context(), []string{"-to", "ada@example.com"}, &stdout, &stderr); err != nil {
		t.Fatalf("%v\n%s%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok: recipient ada@example.com accepted") || !strings.Contains(stdout.String(), "Sent to ada@example.com") {
		t.Errorf("output:\n%s", stdout.String())
	}
	if got := commands(); got != "EHLO MAIL RCPT DATA QUIT" {
		t.Errorf("commands %q", got)
	}
}

func TestSMTPSettings(t *testing.T) {
	clearSMTP(t)
	var out bytes.Buffer
	if err := testEmail(t.Context(), []string{"-to", "ada@example.com"}, &out, &out); err == nil || !strings.Contains(err.Error(), "ALLSTILL_SMTP_HOST") {
		t.Errorf("without a host: %v", err)
	}
	t.Setenv("ALLSTILL_SMTP_HOST", "smtp.example.com")
	t.Setenv("ALLSTILL_SMTP_FROM", "allstill@example.com")
	if err := testEmail(t.Context(), []string{"-to", "not an address"}, &out, &out); err == nil {
		t.Error("a bad -to was taken")
	}
	t.Setenv("ALLSTILL_SMTP_USERNAME", "ada@example.com")
	if err := testEmail(t.Context(), []string{"-to", "ada@example.com"}, &out, &out); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("a user name without a password: %v", err)
	}
	// The password comes from a file, such as a container secret, without
	// its line break.
	file := filepath.Join(t.TempDir(), "password")
	os.WriteFile(file, []byte("not-a-real-password\n"), 0o600)
	t.Setenv("ALLSTILL_SMTP_PASSWORD_FILE", file)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	settings := smtpFlags(fs)
	fs.Parse(nil)
	c, err := settings("allstill.example.com")
	if err != nil || c.Password != "not-a-real-password" || c.Port != 587 || c.Hello != "allstill.example.com" {
		t.Fatalf("%v %+v", err, c)
	}
	for in, want := range map[string]string{"https://allstill.example.com:8443": "allstill.example.com", "": "localhost", "http://192.0.2.1:8000": "[192.0.2.1]"} {
		if got := helloName(in); got != want {
			t.Errorf("hello name of %q: %q, want %q", in, got, want)
		}
	}
	t.Setenv("ALLSTILL_SMTP_PASSWORD", "another")
	if _, err := settings("localhost"); err == nil {
		t.Error("both a password and a password file were taken")
	}
}

// With the Linux package, the settings are in a file only root reads.
func TestTestEmailReadsTheEnvFile(t *testing.T) {
	clearSMTP(t)
	addr, _ := relay(t)
	host, port, _ := net.SplitHostPort(addr)
	file := filepath.Join(t.TempDir(), "allstill.env")
	os.WriteFile(file, []byte("# Email\nALLSTILL_SMTP_HOST="+host+"\nALLSTILL_SMTP_PORT="+port+"\nALLSTILL_SMTP_SECURITY=none\n"+
		"ALLSTILL_SMTP_FROM=\"Allstill <allstill@example.com>\"\n;ALLSTILL_SMTP_USERNAME=commented-out\n"), 0o600)
	var out bytes.Buffer
	if err := testEmail(t.Context(), []string{"-env-file", file, "-to", "ada@example.com"}, &out, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
