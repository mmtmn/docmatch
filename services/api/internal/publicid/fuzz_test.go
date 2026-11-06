package publicid

import (
	"testing"

	"github.com/google/uuid"
)

func FuzzPublicIDRoundTrip(f *testing.F) {
	f.Add([]byte("0123456789abcdef"), "run_00000000000000000000000000")
	f.Add([]byte{}, "run_80000000000000000000000000")
	f.Fuzz(func(t *testing.T, bytes []byte, input string) {
		var id uuid.UUID
		copy(id[:], bytes)
		encoded := Encode("run", id)
		decoded, err := Decode("run", encoded)
		if err != nil || decoded != id {
			t.Fatal("round trip changed identifier")
		}
		if got, err := Decode("run", input); err == nil {
			again, err := Decode("run", Encode("run", got))
			if err != nil || again != got {
				t.Fatal("unstable decode")
			}
		}
	})
}
