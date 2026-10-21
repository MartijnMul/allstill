// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package runner

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type hit struct {
	method, path, rid, values, body string
}

type server struct {
	mu   sync.Mutex
	hits []hit
	code int
	*httptest.Server
}

func newServer(t *testing.T) *server {
	s := &server{code: 200}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits = append(s.hits, hit{r.Method, r.URL.Path, r.URL.Query().Get("rid"), r.Header.Get("Allstill-Values"), string(b)})
		code := s.code
		s.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(s.Close)
	return s
}

func opts(urls []string, cmd ...string) (Options, *bytes.Buffer, *[]string) {
	var out bytes.Buffer
	var warnings []string
	return Options{
		URLs: urls, Command: cmd, Timeout: 2 * time.Second, Tail: 1000,
		Stdout: &out, Stderr: &out, Stdin: strings.NewReader(""), Client: http.DefaultClient,
		Warn: func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) },
	}, &out, &warnings
}

func TestReportsStartAndEnd(t *testing.T) {
	s := newServer(t)
	hc := newServer(t)
	o, out, warnings := opts([]string{s.URL + "/ping/abc", hc.URL + "/ping/def/"}, "sh", "-c",
		`echo backing up; echo "size=18400000000" >> "$ALLSTILL_VALUES_FILE"; echo "Bad Name=1" >> "$ALLSTILL_VALUES_FILE"; exit 3`)
	if code := Run(t.Context(), o); code != 3 {
		t.Fatalf("exit status %d", code)
	}
	if out.String() != "backing up\n" {
		t.Fatalf("output %q", out)
	}
	for _, srv := range []*server{s, hc} {
		if len(srv.hits) != 2 {
			t.Fatalf("hits %+v", srv.hits)
		}
		start, end := srv.hits[0], srv.hits[1]
		if !strings.HasSuffix(start.path, "/start") || start.method != "GET" || start.rid == "" {
			t.Fatalf("start %+v", start)
		}
		if !strings.HasSuffix(end.path, "/3") || end.method != "POST" || end.rid != start.rid ||
			end.body != "backing up\n" || end.values != "size=18400000000" {
			t.Fatalf("end %+v", end)
		}
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "Bad Name") {
		t.Fatalf("warnings %v", *warnings)
	}
}

func TestADeadServerDoesNotHoldTheJob(t *testing.T) {
	s := newServer(t)
	s.Close()
	o, _, warnings := opts([]string{s.URL + "/ping/abc"}, "true")
	o.Timeout = 300 * time.Millisecond
	began := time.Now()
	if code := Run(t.Context(), o); code != 0 {
		t.Fatalf("exit status %d", code)
	}
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
	if len(*warnings) != 2 || strings.Contains((*warnings)[0], "/ping/abc") {
		t.Fatalf("warnings %v", *warnings)
	}
}

func TestServerErrorsAreRetried(t *testing.T) {
	s := newServer(t)
	s.code = 503
	o, _, _ := opts([]string{s.URL + "/ping/abc"}, "true")
	o.Timeout = 1200 * time.Millisecond
	Run(t.Context(), o)
	if len(s.hits) < 4 {
		t.Fatalf("%d hits", len(s.hits))
	}
}

func TestAMissingCommandFails(t *testing.T) {
	s := newServer(t)
	o, _, _ := opts([]string{s.URL + "/ping/abc"}, "no-such-command-anywhere")
	if code := Run(t.Context(), o); code != 127 {
		t.Fatalf("exit status %d", code)
	}
	if end := s.hits[1]; !strings.HasSuffix(end.path, "/127") || !strings.Contains(end.body, "no-such-command") {
		t.Fatalf("end %+v", end)
	}
}

func TestAJobKilledByASignal(t *testing.T) {
	s := newServer(t)
	o, _, _ := opts([]string{s.URL + "/ping/abc"}, "sh", "-c", "kill -TERM $$")
	if code := Run(t.Context(), o); code != 128+15 {
		t.Fatalf("exit status %d", code)
	}
	if end := s.hits[1]; !strings.HasSuffix(end.path, "/143") || !strings.Contains(end.body, "Exit status 143") {
		t.Fatalf("end %+v", end)
	}
}

func TestTail(t *testing.T) {
	tb := &tailBuffer{max: 5}
	tb.Write([]byte("abc"))
	tb.Write([]byte("defgh"))
	if tb.String() != "defgh" {
		t.Fatalf("%q", tb.String())
	}
	tb.Write([]byte("0123456789"))
	if tb.String() != "56789" {
		t.Fatalf("%q", tb.String())
	}
}

func TestAURLWithAQuery(t *testing.T) {
	s := newServer(t)
	var query []string
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, hit{method: r.Method, path: r.URL.Path})
		query = append(query, r.URL.Query().Get("create"))
		s.mu.Unlock()
	})
	o, _, _ := opts([]string{s.URL + "/ping/abcdefghijklmnopqrstuv/backup?create=1"}, "sh", "-c", "exit 3")
	Run(t.Context(), o)
	if s.hits[0].path != "/ping/abcdefghijklmnopqrstuv/backup/start" || s.hits[1].path != "/ping/abcdefghijklmnopqrstuv/backup/3" ||
		query[0] != "1" || query[1] != "1" {
		t.Fatalf("hits %+v, create %v", s.hits, query)
	}
}

func TestABackgroundProcessDoesNotHoldTheEnd(t *testing.T) {
	s := newServer(t)
	o, _, _ := opts([]string{s.URL + "/ping/abc"}, "sh", "-c", "sleep 30 & exit 0")
	began := time.Now()
	if code := Run(t.Context(), o); code != 0 {
		t.Fatalf("exit status %d", code)
	}
	if d := time.Since(began); d > waitDelay+3*time.Second {
		t.Fatalf("took %s", d)
	}
	if len(s.hits) != 2 {
		t.Fatalf("hits %+v", s.hits)
	}
}
