// SPDX-License-Identifier: AGPL-3.0-only

package webpush

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"
)

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of RFC 8291 section 5, with the intermediate values of its
// appendix A.
func TestEncryptRFC8291(t *testing.T) {
	sub, err := ParseSubscription("https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg")
	if err != nil {
		t.Fatal(err)
	}
	as, err := ecdh.P256().NewPrivateKey(mustDecode(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encrypt(sub, []byte("When I grow up, I want to be a watermelon"), as, mustDecode(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64.EncodeToString(body); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
}

func TestEncryptTooLarge(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(nil)
	sub := Subscription{Endpoint: "https://push.example.net/x", P256dh: ua.PublicKey().Bytes(), Auth: make([]byte, 16)}
	as, _ := ecdh.P256().GenerateKey(nil)
	if _, err := encrypt(sub, make([]byte, MaxPayload), as, make([]byte, 16)); err != nil {
		t.Errorf("the largest message: %v", err)
	}
	if _, err := encrypt(sub, make([]byte, MaxPayload+1), as, make([]byte, 16)); err == nil {
		t.Error("a message one octet too large was encrypted")
	}
}

// verify checks a VAPID token's ES256 signature with the base64url public
// key k, and returns its claims.
func verify(t *testing.T, token, k string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d parts", token, len(parts))
	}
	if h := string(mustDecode(t, parts[0])); h != `{"typ":"JWT","alg":"ES256"}` {
		t.Errorf("header %s", h)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustDecode(t, k))
	if err != nil {
		t.Fatal(err)
	}
	sig := mustDecode(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature of %d octets", len(sig))
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the signature does not verify")
	}
	var claims map[string]any
	if err := json.Unmarshal(mustDecode(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// The example of RFC 8292 section 2.4 verifies, which shows verify reads a
// token as push services do. The token and key are the RFC's published
// example, not secrets.
func TestVerifyRFC8292(t *testing.T) {
	token := "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJhdWQiOiJodHRwczovL3" + // gitleaks:allow
		"B1c2guZXhhbXBsZS5uZXQiLCJleHAiOjE0NTM1MjM3NjgsInN1YiI6Im1ha" +
		"Wx0bzpwdXNoQGV4YW1wbGUuY29tIn0.i3CYb7t4xfxCDquptFOepC9GAu_H" +
		"LGkMlMuCGSK2rpiUfnK9ojFwDXb1JrErtmysazNjjvW2L9OkSSHzvoD1oA"
	claims := verify(t, token, "BA1Hxzyi1RUM1b5wjxsn7nGxAszw2u61m164i3MrAIxHF6YK5h4SDYic-dRuU_RCPCfA5aq9ojSwk5Y2EmClBPs") // gitleaks:allow
	if claims["aud"] != "https://push.example.net" || claims["sub"] != "mailto:push@example.com" {
		t.Errorf("claims %v", claims)
	}
}

func TestRequest(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := ecdh.P256().GenerateKey(nil)
	sub, err := ParseSubscription("https://push.example.net:8443/send/abc?x=1", b64.EncodeToString(ua.PublicKey().Bytes()), "AAAAAAAAAAAAAAAAAAAAAA==")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	req, err := key.Request(sub, Message{Payload: []byte("{}"), TTL: time.Hour, Urgency: High}, "https://allstill.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL.String() != sub.Endpoint {
		t.Errorf("%s %s", req.Method, req.URL)
	}
	for h, want := range map[string]string{"Content-Encoding": "aes128gcm", "TTL": "3600", "Urgency": "high"} {
		if got := req.Header.Get(h); got != want {
			t.Errorf("%s: %q, want %q", h, got, want)
		}
	}
	auth, ok := strings.CutPrefix(req.Header.Get("Authorization"), "vapid t=")
	if !ok {
		t.Fatalf("Authorization %q", req.Header.Get("Authorization"))
	}
	token, k, ok := strings.Cut(auth, ", k=")
	if !ok || k != key.PublicKey() {
		t.Fatalf("Authorization %q", req.Header.Get("Authorization"))
	}
	claims := verify(t, token, k)
	if claims["aud"] != "https://push.example.net:8443" || claims["sub"] != "https://allstill.example.com" ||
		claims["exp"] != float64(now.Add(12*time.Hour).Unix()) {
		t.Errorf("claims %v", claims)
	}
	body, _ := io.ReadAll(req.Body)
	if len(body) != 86+2+1+16 {
		t.Errorf("body of %d octets", len(body))
	}
}

func TestAudience(t *testing.T) {
	key, _ := NewKey()
	for endpoint, want := range map[string]string{
		"https://Push.Example.NET:443/x":  "https://push.example.net",
		"https://push.example.net:8443/x": "https://push.example.net:8443",
		"https://[2001:DB8::1]/x":         "https://[2001:db8::1]",
	} {
		token, err := key.token(endpoint, "mailto:ops@example.com", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if claims := verify(t, token, key.PublicKey()); claims["aud"] != want {
			t.Errorf("%s: aud %v, want %s", endpoint, claims["aud"], want)
		}
	}
	ua, _ := ecdh.P256().GenerateKey(nil)
	sub := Subscription{Endpoint: "https://push.example.net/x", P256dh: ua.PublicKey().Bytes(), Auth: make([]byte, 16)}
	if _, err := key.Request(sub, Message{}, "http://localhost:8000", time.Now()); err == nil {
		t.Error("an http subject was accepted")
	}
	req, err := key.Request(sub, Message{TTL: -time.Second}, "mailto:ops@example.com", time.Now())
	if err != nil || req.Header.Get("TTL") != "0" {
		t.Errorf("a negative TTL: %v, %q", err, req.Header.Get("TTL"))
	}
}

func TestKeyRoundTrip(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseKey(key.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if again.PublicKey() != key.PublicKey() {
		t.Error("the public key changed")
	}
	if _, err := ParseKey(make([]byte, 32)); err == nil {
		t.Error("a zero key was accepted")
	}
}

func TestParseSubscription(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(nil)
	p256dh := b64.EncodeToString(ua.PublicKey().Bytes())
	auth := b64.EncodeToString([]byte("sixteen octets!!"))
	for _, c := range []struct {
		name, endpoint, p256dh, auth string
	}{
		{"http", "http://push.example.net/x", p256dh, auth},
		{"no host", "https:///x", p256dh, auth},
		{"user info", "https://a:b@push.example.net/x", p256dh, auth},
		{"zone", "https://[fe80::1%251]/x", p256dh, auth},
		{"too long", "https://push.example.net/" + strings.Repeat("x", 1024), p256dh, auth},
		{"key not on the curve", "https://push.example.net/x", b64.EncodeToString(append([]byte{4}, make([]byte, 64)...)), auth},
		{"key not base64url", "https://push.example.net/x", "not+base64/", auth},
		{"short auth", "https://push.example.net/x", p256dh, "AAAA"},
	} {
		if _, err := ParseSubscription(c.endpoint, c.p256dh, c.auth); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := ParseSubscription("https://push.example.net/x", p256dh+"=", auth+"=="); err != nil {
		t.Errorf("padded keys: %v", err)
	}
}

// A subscription comes from a browser, so from anyone signed in.
func FuzzParseSubscription(f *testing.F) {
	f.Add("https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg")
	f.Add("https://push.example.net/x", "", "")
	f.Fuzz(func(t *testing.T, endpoint, p256dh, auth string) {
		sub, err := ParseSubscription(endpoint, p256dh, auth)
		if err != nil {
			return
		}
		if len(sub.P256dh) != 65 || len(sub.Auth) != 16 || !strings.HasPrefix(sub.Endpoint, "https://") {
			t.Errorf("accepted %+v", sub)
		}
		as, _ := ecdh.P256().GenerateKey(nil)
		if _, err := encrypt(sub, []byte("{}"), as, make([]byte, 16)); err != nil {
			t.Errorf("an accepted subscription cannot be encrypted for: %v", err)
		}
	})
}

func TestClassify(t *testing.T) {
	for status, want := range map[int]Outcome{
		201: Sent, 202: Sent, 404: Gone, 410: Gone, 429: Retry, 500: Retry, 503: Retry,
		400: Refused, 401: Refused, 403: Refused, 413: Refused,
	} {
		if got := Classify(status); got != want {
			t.Errorf("%d: %v, want %v", status, got, want)
		}
	}
}
