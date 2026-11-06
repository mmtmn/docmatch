package crypto

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func FuzzSignedToken(f *testing.F) {
	f.Add([]byte("evidence"), "invalid", int64(60))
	f.Fuzz(func(t *testing.T, payload []byte, arbitrary string, seconds int64) {
		if len(payload) > 4096 || len(arbitrary) > 8192 {
			t.Skip()
		}
		key := bytes.Repeat([]byte{7}, 32)
		now := time.Unix(1700000000, 0)
		exp := now.Add(time.Duration(seconds%3600+3601) * time.Second)
		token := SignToken(key, payload, exp)
		got, err := VerifyToken(key, token, now)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatal("signed payload changed")
		}
		if _, err := VerifyToken(key, token, exp); !errors.Is(err, ErrTokenExpired) {
			t.Fatal("expired token accepted")
		}
		wrongKey := bytes.Repeat([]byte{8}, 32)
		if _, err := VerifyToken(wrongKey, token, now); err == nil {
			t.Fatal("wrong signing key accepted")
		}
		_, _ = VerifyToken(key, arbitrary, now)
	})
}
