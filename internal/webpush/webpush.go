// SPDX-License-Identifier: AGPL-3.0-only

// Package webpush sends Web Push messages: encrypted for one browser
// (RFC 8291, with the aes128gcm content coding of RFC 8188) and signed with
// the installation's VAPID key (RFC 8292), on the standard library alone
// (ADR 0001). It builds requests and reads answers; sending them, through a
// client that keeps out of the server's own network, is the caller's job.
package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxPayload is the most plaintext one message carries: a push service need
// accept no more than 4096 octets of body, of which the header takes 86, the
// padding delimiter 1 and the authentication tag 16 (RFC 8291 section 4).
const MaxPayload = 4096 - 86 - 1 - 16

const recordSize = 4096

var b64 = base64.RawURLEncoding

// Subscription is what a browser hands out for one device: where to push,
// and the keys to encrypt for it. The endpoint is a secret: whoever holds it
// and the VAPID key can push to the device.
type Subscription struct {
	Endpoint string
	P256dh   []byte // the browser's public key, an uncompressed P-256 point
	Auth     []byte // the authentication secret, 16 octets
}

// ParseSubscription reads the endpoint and the two keys, base64url encoded,
// as a browser's PushSubscription.toJSON() gives them. The endpoint must be
// https.
func ParseSubscription(endpoint, p256dh, auth string) (Subscription, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(u.Host, "%") {
		return Subscription{}, errors.New("the endpoint is not an https address")
	}
	if len(endpoint) > 1024 {
		return Subscription{}, errors.New("the endpoint is longer than 1024 characters")
	}
	pub, err := decode(p256dh)
	if err != nil {
		return Subscription{}, errors.New("the p256dh key is not base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return Subscription{}, errors.New("the p256dh key is not a P-256 public key")
	}
	secret, err := decode(auth)
	if err != nil || len(secret) != 16 {
		return Subscription{}, errors.New("the auth secret is not 16 octets of base64url")
	}
	return Subscription{Endpoint: endpoint, P256dh: pub, Auth: secret}, nil
}

// decode reads base64url with or without padding, as browsers differ.
func decode(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// Key is an installation's VAPID key pair (RFC 8292): it signs every push,
// and a browser subscribes for its public key.
type Key struct {
	private *ecdsa.PrivateKey
}

// NewKey makes a key pair.
func NewKey() (*Key, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Key{k}, nil
}

// ParseKey reads a private key as Bytes wrote it.
func ParseKey(b []byte) (*Key, error) {
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), b)
	if err != nil {
		return nil, fmt.Errorf("reading the VAPID key: %w", err)
	}
	return &Key{k}, nil
}

// Bytes is the private key as its 32-octet scalar.
func (k *Key) Bytes() []byte {
	b, err := k.private.Bytes()
	if err != nil {
		panic(err) // a P-256 key always has a scalar
	}
	return b
}

// PublicKey is the public key as an uncompressed point, base64url encoded:
// the applicationServerKey a browser subscribes with, and the k of every push.
func (k *Key) PublicKey() string {
	b, err := k.private.PublicKey.Bytes()
	if err != nil {
		panic(err) // a P-256 key always has a point
	}
	return b64.EncodeToString(b)
}

// Urgency is how soon a push service should deliver (RFC 8030 section 5.3).
type Urgency string

const (
	Normal Urgency = "normal"
	High   Urgency = "high"
)

// Message is one push.
type Message struct {
	Payload []byte
	// TTL is how long the push service keeps the message for a device that
	// is offline; zero means it is dropped unless it can go at once.
	TTL     time.Duration
	Urgency Urgency
}

// Request builds the push of m to sub, signed for subject, the https or
// mailto address that tells push services whom to contact.
func (k *Key) Request(sub Subscription, m Message, subject string, now time.Time) (*http.Request, error) {
	if !strings.HasPrefix(subject, "https://") && !strings.HasPrefix(subject, "mailto:") {
		return nil, errors.New("the VAPID subject must be an https or mailto address")
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	body, err := encrypt(sub, m.Payload, as, salt)
	if err != nil {
		return nil, err
	}
	token, err := k.token(sub.Endpoint, subject, now)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("the endpoint cannot be read") // its error would hold the endpoint
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(max(m.TTL, 0)/time.Second)))
	if m.Urgency != "" {
		req.Header.Set("Urgency", string(m.Urgency))
	}
	req.Header.Set("Authorization", "vapid t="+token+", k="+k.PublicKey())
	return req, nil
}

// tokenLifetime is how long a VAPID token is valid; RFC 8292 allows at most
// 24 hours.
const tokenLifetime = 12 * time.Hour

// token is the VAPID JWT for the push service of endpoint (RFC 8292
// section 2): ES256, with the origin of the push service as audience.
func (k *Key) token(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("the endpoint cannot be read") // its error would hold the endpoint
	}
	// The audience is the push service's origin as RFC 6454 writes it: the
	// host in lower case, without the default port.
	aud := "https://" + strings.ToLower(u.Hostname())
	if strings.Contains(u.Hostname(), ":") {
		aud = "https://[" + strings.ToLower(u.Hostname()) + "]"
	}
	if p := u.Port(); p != "" && p != "443" {
		aud += ":" + p
	}
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{aud, now.Add(tokenLifetime).Unix(), subject})
	if err != nil {
		return "", err
	}
	signed := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signed + "." + b64.EncodeToString(sig), nil
}

// encrypt makes the body of a push: the aes128gcm header (salt, record size
// and the sender's ephemeral public key) and one record holding the plaintext
// and its padding delimiter (RFC 8291 sections 3.4 and 4).
func encrypt(sub Subscription, plaintext []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("the message is %d octets, more than the %d a push may hold", len(plaintext), MaxPayload)
	}
	ua, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, err
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := as.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), sub.P256dh...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, secret, sub.Auth, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, 86)
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	record := append(append([]byte{}, plaintext...), 2) // the last record's delimiter, no padding
	return gcm.Seal(header, nonce, record, nil), nil
}

// Outcome is what an answer from a push service means for a subscription.
type Outcome int

const (
	// Sent: the push service accepted the message.
	Sent Outcome = iota
	// Retry: try again later; the push service was busy or failed.
	Retry
	// Gone: the subscription no longer exists and is removed. A push service
	// that refuses the VAPID key may answer this too (Mozilla's answers 404),
	// so it only counts when the subscription was made for the current key.
	Gone
	// Refused: the push service will not take this message as it is, such as
	// one too large or with a token it does not accept.
	Refused
)

// Classify reads a push service's answer (RFC 8030 sections 5 and 8).
func Classify(status int) Outcome {
	switch {
	case status >= 200 && status < 300:
		return Sent
	case status == http.StatusNotFound || status == http.StatusGone:
		return Gone
	case status == http.StatusTooManyRequests || status >= 500:
		return Retry
	default:
		return Refused
	}
}
