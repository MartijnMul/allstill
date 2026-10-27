// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/webpush"
)

// device is a browser with a push subscription at a fake push service, which
// answers with status and decrypts what it is sent, as the browser would.
type device struct {
	t      *testing.T
	srv    *httptest.Server
	ua     *ecdh.PrivateKey
	auth   []byte
	mu     sync.Mutex
	status int
	got    []payload
	auths  []string
}

func newDevice(t *testing.T) *device {
	d := &device{t: t, status: http.StatusCreated, auth: make([]byte, 16)}
	d.ua, _ = ecdh.P256().GenerateKey(rand.Reader)
	rand.Read(d.auth)
	d.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		d.auths = append(d.auths, r.Header.Get("Authorization"))
		if r.Header.Get("Content-Encoding") != "aes128gcm" {
			t.Errorf("Content-Encoding %q", r.Header.Get("Content-Encoding"))
		}
		if d.status < 300 {
			var m payload
			if err := json.Unmarshal(d.decrypt(body), &m); err != nil {
				t.Errorf("reading a push: %v", err)
			}
			d.got = append(d.got, m)
		}
		w.WriteHeader(d.status)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *device) subscription() webpush.Subscription {
	return webpush.Subscription{Endpoint: d.srv.URL + "/push/abc", P256dh: d.ua.PublicKey().Bytes(), Auth: d.auth}
}

// decrypt reads a push as a browser does (RFC 8291 section 3.4).
func (d *device) decrypt(body []byte) []byte {
	d.t.Helper()
	salt, keyLen := body[:16], int(body[20])
	asPublic := body[21 : 21+keyLen]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		d.t.Fatal(err)
	}
	secret, _ := d.ua.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), d.ua.PublicKey().Bytes()...), asPublic...)
	ikm, _ := hkdf.Key(sha256.New, secret, d.auth, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+keyLen:], nil)
	if err != nil {
		d.t.Fatal(err)
	}
	if plain[len(plain)-1] != 2 {
		d.t.Fatalf("padding delimiter %d", plain[len(plain)-1])
	}
	return plain[:len(plain)-1]
}

func (d *device) answer(status int) {
	d.mu.Lock()
	d.status = status
	d.mu.Unlock()
}

func (d *device) titles() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, m := range d.got {
		out = append(out, m.Title)
	}
	return out
}

func (d *device) requests() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.auths)
}

// withPush gives the world's sender a key, and the administrator the device,
// subscribed for key (the sender's when empty).
func (w *world) withPush(d *device, key string) string {
	w.t.Helper()
	ctx := w.t.Context()
	k, err := w.st.VAPIDKey(ctx, w.ws)
	if err != nil {
		w.t.Fatal(err)
	}
	w.sender.Push = &Push{Key: k, Base: "https://allstill.example.com", Client: d.srv.Client()}
	if key == "" {
		key = k.PublicKey()
	}
	c, err := w.st.SavePush(ctx, w.ws, w.admin, "Phone", d.subscription(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c.ID
}

// confirm sends the device a test and opens its link.
func (w *world) confirm(d *device, channelID string) {
	w.t.Helper()
	ctx := w.t.Context()
	if err := w.st.Test(ctx, w.admin, channelID, w.now); err != nil {
		w.t.Fatal(err)
	}
	if err := w.sender.Pass(ctx); err != nil {
		w.t.Fatal(err)
	}
	d.mu.Lock()
	link := d.got[len(d.got)-1].URL
	d.mu.Unlock()
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(link, "https://allstill.example.com/channels/"+channelID+"/confirm?") {
		w.t.Fatalf("test link %q", link)
	}
	if err := w.st.Confirm(ctx, w.admin, channelID, u.Query().Get("token"), w.now); err != nil {
		w.t.Fatal(err)
	}
}

func alertsOn(w *world) {
	if err := w.st.SetSetting(w.t.Context(), "alerts", "on"); err != nil {
		w.t.Fatal(err)
	}
}

func TestAnAlertIsPushedToAConfirmedDevice(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	w.at("12:00", backup, check.Fail)
	w.at("12:30", backup, check.Success)
	w.at("12:41", "", "")
	w.want("test sent: ", "alert sent: down backup is down", "alert sent: recovered backup is down")
	if got := strings.Join(d.titles(), "|"); got != "Allstill test message|backup is down|Resolved: backup is down" {
		t.Errorf("pushed %s", got)
	}
	// Every push is signed with the installation's key, for the push
	// service's origin.
	k, _ := w.st.VAPIDKey(t.Context(), w.ws)
	for _, a := range d.auths {
		if !strings.HasPrefix(a, "vapid t=") || !strings.HasSuffix(a, ", k="+k.PublicKey()) {
			t.Errorf("Authorization %q", a)
		}
		claims, _ := base64.RawURLEncoding.DecodeString(strings.Split(strings.TrimPrefix(a, "vapid t="), ".")[1])
		var c struct{ Aud, Sub string }
		json.Unmarshal(claims, &c)
		if c.Aud != strings.TrimSuffix(d.srv.URL, "/") || c.Sub != "https://allstill.example.com" {
			t.Errorf("claims %s", claims)
		}
	}
}

func TestAnUnconfirmedDeviceGetsNoAlerts(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	w.withPush(d, "")
	backup := w.check("Shop", "backup")
	w.at("12:00", backup, check.Fail)
	w.want("alert failed: down backup is down")
	if d.requests() != 0 {
		t.Errorf("%d pushes to an unconfirmed device", d.requests())
	}
}

func TestShadowModeSendsOnlyTests(t *testing.T) {
	w := newWorld(t)
	d := newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	w.at("12:00", backup, check.Fail)
	w.want("test sent: ", "alert shadow: down backup is down")
	// Forced, shadow mode holds whatever the database says.
	alertsOn(w)
	w.sender.Shadow = true
	other := w.check("Shop", "web")
	w.at("12:10", other, check.Fail)
	if d.requests() != 1 {
		t.Errorf("%d pushes in shadow mode, want the test only", d.requests())
	}
}

func TestADeviceThatUnsubscribedIsRemoved(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	d.answer(http.StatusGone)
	w.at("12:00", backup, check.Fail)
	w.want("test sent: ", "alert failed: down backup is down")
	if cs, _ := w.st.Channels(t.Context(), w.admin); len(cs) != 0 {
		t.Errorf("the channel is still there: %+v", cs)
	}
}

func TestAFailedPushIsTriedOnceMore(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	channel := w.withPush(d, "")
	w.confirm(d, channel)
	d.answer(http.StatusServiceUnavailable)
	w.at("12:00", backup, check.Fail)
	w.want("test sent: ", "alert pending: down backup is down")
	w.at("12:00", "", "") // not before the minute is up
	if d.requests() != 2 {
		t.Errorf("%d requests, want the test and one try", d.requests())
	}
	w.at("12:01", "", "")
	w.want("test sent: ", "alert failed: down backup is down")
	cs, _ := w.st.Channels(t.Context(), w.admin)
	if len(cs) != 1 || cs[0].FailedAt.IsZero() {
		t.Errorf("the channel is not marked as failing: %+v", cs)
	}
	// A device that works again is confirmed with a new test.
	d.answer(http.StatusCreated)
	w.confirm(d, channel)
	if cs, _ := w.st.Channels(t.Context(), w.admin); !cs[0].FailedAt.IsZero() {
		t.Error("a confirmed channel is still marked as failing")
	}
}

func TestASubscriptionForAnotherKeyIsNeverPushedTo(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	other, _ := webpush.NewKey()
	channel := w.withPush(d, other.PublicKey())
	w.set("11:00")
	if err := w.st.Test(t.Context(), w.admin, channel, w.now); err != nil {
		t.Fatal(err)
	}
	w.at("11:00", "", "")
	w.want("test failed: ")
	if d.requests() != 0 {
		t.Errorf("%d pushes signed with a key the device did not subscribe for", d.requests())
	}
	if cs, _ := w.st.Channels(t.Context(), w.admin); len(cs) != 1 {
		t.Error("the channel was removed instead of kept for the device to renew")
	}
}

func TestAPushFitsInFourKilobytes(t *testing.T) {
	m := payload{Title: "200 changes", Body: strings.Repeat("a check with a long name is down\n", 200)}
	if b := encode(m); len(b) > webpush.MaxPayload {
		t.Errorf("%d octets", len(b))
	}
}

// What waits for a second try is checked again when it goes.
func TestAWaitingPushIsCheckedAgain(t *testing.T) {
	t.Run("shadow mode came on", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.at("12:00", backup, check.Fail)
		d.answer(http.StatusCreated)
		w.sender.Shadow = true
		w.at("12:01", "", "")
		w.want("test sent: ", "alert shadow: down backup is down")
		// Its recovery is not pushed either, once alerts are on again: the
		// down was heard in shadow mode only.
		w.sender.Shadow = false
		if _, err := w.st.Ping(t.Context(), store.PingIn{CheckUUID: backup, Kind: check.Success, ExitStatus: -1, Method: "POST", At: w.now}); err != nil {
			t.Fatal(err)
		}
		w.at("12:20", "", "")
		if got := strings.Join(d.titles(), "|"); got != "Allstill test message" {
			t.Errorf("pushed %s", got)
		}
	})
	t.Run("the device changed hands", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.at("12:00", backup, check.Fail)
		d.answer(http.StatusCreated)
		bo, _ := w.st.CreatePerson(t.Context(), w.ws, store.NewPerson{Name: "Bo", TimeZone: "Europe/Amsterdam"})
		k, _ := w.st.VAPIDKey(t.Context(), w.ws)
		if _, err := w.st.SavePush(t.Context(), w.ws, bo, "Phone", d.subscription(), k.PublicKey()); err != nil {
			t.Fatal(err)
		}
		w.at("12:01", "", "")
		w.want("test sent: ", "alert failed: down backup is down")
		if got := strings.Join(d.titles(), "|"); got != "Allstill test message" {
			t.Errorf("pushed %s", got)
		}
	})
	t.Run("the incident resolved", func(t *testing.T) {
		w := newWorld(t)
		alertsOn(w)
		d := newDevice(t)
		backup := w.check("Shop", "backup")
		w.set("11:00")
		w.confirm(d, w.withPush(d, ""))
		d.answer(http.StatusServiceUnavailable)
		w.at("12:00", backup, check.Fail)
		d.answer(http.StatusCreated)
		// Allstill was busy or down until well after the check recovered.
		if _, err := w.st.Ping(t.Context(), store.PingIn{CheckUUID: backup, Kind: check.Success, ExitStatus: -1, Method: "POST", At: w.now.Add(5 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		w.at("12:20", "", "")
		// Neither the down, which is no longer true, nor a recovery of a
		// down the person never heard.
		if got := strings.Join(d.titles(), "|"); got != "Allstill test message" {
			t.Errorf("pushed %s", got)
		}
		w.want("test sent: ", "alert failed: down backup is down")
	})
}

// A down one device got was heard, though another device's copy was dropped:
// the recovery follows on the devices.
func TestADownHeardOnOneDeviceStaysHeard(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	phone, laptop := newDevice(t), newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(phone, w.withPush(phone, ""))
	k, _ := w.st.VAPIDKey(t.Context(), w.ws)
	c, err := w.st.SavePush(t.Context(), w.ws, w.admin, "Laptop", laptop.subscription(), k.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	// Each device's fake service has its own certificate; the sender trusts
	// both through one pool.
	w.sender.Push.Client = trusting(phone, laptop)
	w.confirm(laptop, c.ID)
	laptop.answer(http.StatusServiceUnavailable)
	w.at("12:00", backup, check.Fail)
	laptop.answer(http.StatusCreated)
	if _, err := w.st.Ping(t.Context(), store.PingIn{CheckUUID: backup, Kind: check.Success, ExitStatus: -1, Method: "POST", At: w.now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	w.at("12:20", "", "")
	if got := strings.Join(phone.titles(), "|"); got != "Allstill test message|backup is down|Resolved: backup is down" {
		t.Errorf("the phone got %s", got)
	}
}

// trusting is a client that trusts the fake push services of the devices.
func trusting(devices ...*device) *http.Client {
	pool := x509.NewCertPool()
	for _, d := range devices {
		pool.AddCert(d.srv.Certificate())
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func TestRetryComesBeforeGather(t *testing.T) {
	if RetryAfter >= Gather {
		t.Fatalf("RetryAfter %v must be shorter than Gather %v: a down's second try must settle before its recovery is decided", RetryAfter, Gather)
	}
}

// A digest that fell in shadow mode told nothing about what it listed as
// still open: the recovery of a down heard for real still comes.
func TestAShadowedDigestKeepsWhatWasHeard(t *testing.T) {
	w := newWorld(t)
	alertsOn(w)
	d := newDevice(t)
	backup := w.check("Shop", "backup")
	w.set("11:00")
	w.confirm(d, w.withPush(d, ""))
	w.at("12:00", backup, check.Fail)
	d.answer(http.StatusServiceUnavailable)
	w.at("22 08:00", "", "") // the digest waits for its second try
	d.answer(http.StatusCreated)
	w.sender.Shadow = true
	w.at("22 08:01", "", "")
	w.sender.Shadow = false
	if _, err := w.st.Ping(t.Context(), store.PingIn{CheckUUID: backup, Kind: check.Success, ExitStatus: -1, Method: "POST", At: w.now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	w.at("22 08:13", "", "")
	if got := strings.Join(d.titles(), "|"); got != "Allstill test message|backup is down|Resolved: backup is down" {
		t.Errorf("pushed %s", got)
	}
	w.want("test sent: ", "alert sent: down backup is down", "digest shadow: still_open backup is down", "alert sent: recovered backup is down")
}

// A sender that has not acted on what waits is behind by as much, less the
// time between passes (R11).
func TestBehind(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("12:00", "", "")
	if b, err := w.sender.Behind(t.Context()); err != nil || b != 0 {
		t.Fatalf("with nothing waiting: %v, %v", b, err)
	}
	// The check goes down, and the sender does not look for ten minutes.
	w.set("12:00")
	if _, err := w.st.Ping(t.Context(), store.PingIn{CheckUUID: backup, Kind: check.Fail, ExitStatus: 1, Method: "POST", At: w.now}); err != nil {
		t.Fatal(err)
	}
	w.set("12:10")
	if b, _ := w.sender.Behind(t.Context()); b != 10*time.Minute-Every {
		t.Fatalf("behind %v, want %v", b, 10*time.Minute-Every)
	}
	w.at("12:10", "", "")
	if b, _ := w.sender.Behind(t.Context()); b != 0 {
		t.Fatalf("after a pass: behind %v", b)
	}
	// A sender whose last good pass was long ago is behind by as much.
	w.sender.lastPass.Store(w.now.Add(-5 * time.Minute).UnixMilli())
	if b, _ := w.sender.Behind(t.Context()); b != 5*time.Minute-Every {
		t.Fatalf("with no good pass for 5 min: behind %v", b)
	}
}

// Changing a check's alerting, or moving it, makes the sender look again at
// once; that is not lagging since the incident opened.
func TestAChangeDuringAnOldIncidentIsNotBehind(t *testing.T) {
	w := newWorld(t)
	backup := w.check("Shop", "backup")
	w.at("22 03:00", backup, check.Fail) // waits for the end of quiet hours
	w.set("22 05:00")
	views, _ := w.st.Checks(t.Context(), w.access)
	if err := w.st.SetAlerting(t.Context(), w.ws, views[0].ID, "digest", false, w.now); err != nil {
		t.Fatal(err)
	}
	if b, _ := w.sender.Behind(t.Context()); b != 0 {
		t.Fatalf("after a change of alerting: behind %v", b)
	}
	w.at("22 05:00", "", "")
	// Moved to a project with a new person, who becomes a recipient.
	lab, _, _ := w.st.CreateProject(t.Context(), w.ws, "Lab")
	bo, _ := w.st.CreatePerson(t.Context(), w.ws, store.NewPerson{Name: "Bo", TimeZone: "Europe/Amsterdam"})
	w.st.SetMember(t.Context(), w.ws, lab, bo, store.Editor)
	w.set("22 06:00")
	if err := w.st.MoveCheck(t.Context(), w.ws, views[0].ID, lab, w.now); err != nil {
		t.Fatal(err)
	}
	if b, _ := w.sender.Behind(t.Context()); b != 0 {
		t.Fatalf("after a move: behind %v", b)
	}
}
