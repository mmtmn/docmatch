package config

import (
	"fmt"
	"log/slog"
)

// Redacted is what every secret prints as, whatever the formatting path.
const Redacted = "[redigido]"

// Secret is a string that never leaks through formatting, logging or JSON.
// Call Reveal only at the point of use (signing, connecting, authenticating).
type Secret string

// Reveal returns the underlying value.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether the secret is non-empty.
func (s Secret) IsSet() bool { return s != "" }

// String implements fmt.Stringer with the redaction marker.
func (s Secret) String() string { return Redacted }

// GoString implements fmt.GoStringer so %#v does not expose the value.
func (s Secret) GoString() string { return "config.Secret(" + Redacted + ")" }

// Format implements fmt.Formatter: it takes precedence over every verb,
// including ones that are invalid for strings (%d, %x), which would otherwise
// bypass String() and print the raw bytes inside a "%!d(...)" marker.
func (s Secret) Format(f fmt.State, verb rune) {
	switch verb {
	case 'T':
		_, _ = f.Write([]byte("config.Secret"))
	default:
		_, _ = f.Write([]byte(Redacted))
	}
}

// LogValue implements slog.LogValuer so structured logs show the marker.
func (s Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }

// MarshalText implements encoding.TextMarshaler (JSON, YAML) with the marker.
func (s Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// MarshalJSON keeps JSON encoders from ever serializing the real value.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// SecretBytes is a byte slice with the same redaction guarantees as Secret.
type SecretBytes []byte

// Reveal returns the underlying bytes.
func (s SecretBytes) Reveal() []byte { return []byte(s) }

// IsSet reports whether the secret is non-empty.
func (s SecretBytes) IsSet() bool { return len(s) > 0 }

// String implements fmt.Stringer with the redaction marker.
func (s SecretBytes) String() string { return Redacted }

// GoString implements fmt.GoStringer so %#v does not expose the bytes.
func (s SecretBytes) GoString() string { return "config.SecretBytes(" + Redacted + ")" }

// Format implements fmt.Formatter for every verb (see Secret.Format).
func (s SecretBytes) Format(f fmt.State, verb rune) {
	switch verb {
	case 'T':
		_, _ = f.Write([]byte("config.SecretBytes"))
	default:
		_, _ = f.Write([]byte(Redacted))
	}
}

// LogValue implements slog.LogValuer so structured logs show the marker.
func (s SecretBytes) LogValue() slog.Value { return slog.StringValue(Redacted) }

// MarshalText implements encoding.TextMarshaler with the marker.
func (s SecretBytes) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// MarshalJSON keeps JSON encoders from ever serializing the bytes.
func (s SecretBytes) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }
