// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// authenticator is a passkey in software, as a phone or laptop holds one: it
// makes a key pair for a site and signs challenges with it, so the tests run
// the real WebAuthn checks.
type authenticator struct {
	t          *testing.T
	origin     string
	key        *ecdsa.PrivateKey
	id         []byte
	userHandle []byte
	count      uint32
	synced     bool // keeps its counter at 0, as synced passkeys do
}

var b64 = base64.RawURLEncoding

// Flags: user present, user verified, backup eligible and backed up (a
// synced passkey), attested credential data.
const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagBS = 0x10
	flagAT = 0x40
)

type options struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		UserVerification       string `json:"userVerification"`
		AuthenticatorSelection struct {
			ResidentKey      string `json:"residentKey"`
			UserVerification string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		ExcludeCredentials []struct {
			ID string `json:"id"`
		} `json:"excludeCredentials"`
	} `json:"publicKey"`
}

func (a *authenticator) clientData(kind, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	return b
}

// create makes a new passkey for the options a server sent.
func (a *authenticator) create(raw []byte) map[string]any {
	a.t.Helper()
	var o options
	if err := json.Unmarshal(raw, &o); err != nil {
		a.t.Fatalf("creation options %s: %v", raw, err)
	}
	if o.PublicKey.AuthenticatorSelection.ResidentKey != "required" || o.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		a.t.Fatalf("the server asks for %+v", o.PublicKey.AuthenticatorSelection)
	}
	a.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	a.id = make([]byte, 16)
	rand.Read(a.id)
	a.userHandle, _ = b64.DecodeString(o.PublicKey.User.ID)

	pub, _ := a.key.PublicKey.Bytes() // 0x04 || x || y
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: pub[1:33], -3: pub[33:]})
	auth := a.authData(o.PublicKey.RP.ID, flagUP|flagUV|flagBE|flagBS|flagAT)
	auth = append(auth, make([]byte, 16)...) // AAGUID
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(a.id)))
	auth = append(auth, a.id...)
	auth = append(auth, cose...)
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	return map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
	}
}

// get signs a sign-in challenge.
func (a *authenticator) get(raw []byte) map[string]any {
	a.t.Helper()
	var o options
	if err := json.Unmarshal(raw, &o); err != nil {
		a.t.Fatalf("request options %s: %v", raw, err)
	}
	if o.PublicKey.UserVerification != "required" {
		a.t.Fatalf("user verification %q", o.PublicKey.UserVerification)
	}
	return a.sign(o.PublicKey.RPID, o.PublicKey.Challenge)
}

func (a *authenticator) sign(rpID, challenge string) map[string]any {
	if !a.synced {
		a.count++
	}
	auth := a.authData(rpID, flagUP|flagUV|flagBE|flagBS)
	data := a.clientData("webauthn.get", challenge)
	digest := sha256.Sum256(data)
	msg := sha256.Sum256(append(append([]byte{}, auth...), digest[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, a.key, msg[:])
	return map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(data),
			"authenticatorData": b64.EncodeToString(auth),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.userHandle),
		},
	}
}

func (a *authenticator) authData(rpID string, flags byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	b := append(h[:], flags)
	return binary.BigEndian.AppendUint32(b, a.count)
}
