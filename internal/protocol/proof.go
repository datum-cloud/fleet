// SPDX-License-Identifier: Apache-2.0

// Package protocol defines the Fleet-specific client proof. It is deliberately
// independent of Compute's provisional internal protocol.
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	Version   = "redfish-v1alpha1"
	Domain    = "datum-fleet-machine-management-v1"
	ClockSkew = 30 * time.Second
)

type Proof struct {
	Timestamp string
	Nonce     string
	Signature string
}

func PublicKey(key ed25519.PrivateKey) string {
	return hex.EncodeToString(key.Public().(ed25519.PublicKey))
}

func Sign(key ed25519.PrivateKey, sessionUID, audience string, now time.Time) (Proof, error) {
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return Proof{}, err
	}
	p := Proof{Timestamp: strconv.FormatInt(now.Unix(), 10), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
	p.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, message(sessionUID, audience, p.Timestamp, p.Nonce)))
	return p, nil
}

func Verify(publicKey, sessionUID, audience string, p Proof, now time.Time, usedNonce func(string) bool) error {
	pub, err := hex.DecodeString(publicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid session client public key")
	}
	ts, err := strconv.ParseInt(p.Timestamp, 10, 64)
	if err != nil {
		return errors.New("missing or malformed proof timestamp")
	}
	d := now.Sub(time.Unix(ts, 0))
	if d > ClockSkew || d < -ClockSkew {
		return fmt.Errorf("proof timestamp outside %s clock skew", ClockSkew)
	}
	if p.Nonce == "" {
		return errors.New("missing proof nonce")
	}
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil {
		return errors.New("malformed proof signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), message(sessionUID, audience, p.Timestamp, p.Nonce), sig) {
		return errors.New("proof signature does not match session key and gateway audience")
	}
	if usedNonce != nil && usedNonce(p.Nonce) {
		return errors.New("proof nonce was already consumed")
	}
	return nil
}

func message(sessionUID, audience, timestamp, nonce string) []byte {
	return []byte(strings.Join([]string{Domain, Version, sessionUID, audience, timestamp, nonce}, "\n"))
}
