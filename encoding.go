package neffos

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
	"time"
)

type (
	// MessageObjectMarshaler is an optional interface that "objects"
	// can implement to customize their byte representation, see `Marshal` package-level function.
	MessageObjectMarshaler interface {
		Marshal() ([]byte, error)
	}

	// MessageObjectUnmarshaler is an optional interface that "objects"
	// can implement to customize their structure, see `Message.Unmarshal` and `Message.As`.
	MessageObjectUnmarshaler interface {
		Unmarshal(body []byte) error
	}

	// Marshaler is the type of `DefaultMarshaler`: it encodes a value to a message body.
	Marshaler func(v any) ([]byte, error)

	// Unmarshaler is the type of `DefaultUnmarshaler`: it decodes a message body into "outPtr".
	Unmarshaler func(body []byte, outPtr any) error
)

// marshalDurationAsNanos and unmarshalDurationFromNanos keep the encoding/json v1
// representation of time.Duration, a plain int64 nanosecond count. encoding/json/v2
// has no default representation for time.Duration and would fail on every struct
// that carries one.
var (
	marshalDurationAsNanos = json.WithMarshalers(json.MarshalFunc(func(d time.Duration) ([]byte, error) {
		return strconv.AppendInt(nil, int64(d), 10), nil
	}))

	unmarshalDurationFromNanos = json.WithUnmarshalers(json.UnmarshalFunc(func(b []byte, d *time.Duration) error {
		i, err := strconv.ParseInt(string(b), 10, 64)
		if err != nil {
			return err
		}
		*d = time.Duration(i)
		return nil
	}))
)

var (
	// JSONMarshalOptions are the encoding/json/v2 options the `DefaultMarshaler` uses:
	// map keys sorted (byte-stable bodies), invalid UTF-8 replaced by U+FFFD instead of
	// failing the message, and time.Duration as int64 nanoseconds. Everything else is
	// the v2 default: a nil slice encodes as [] and a nil map as {}, `omitempty` omits
	// empty JSON values only (use `omitzero` for Go zero values), and <, > and & are
	// not HTML-escaped. Reassign to change the policy, for example
	// `json.JoinOptions(neffos.JSONMarshalOptions, json.FormatNilSliceAsNull(true))`.
	JSONMarshalOptions = json.JoinOptions(
		json.Deterministic(true),
		jsontext.AllowInvalidUTF8(true),
		marshalDurationAsNanos,
	)

	// JSONUnmarshalOptions are the encoding/json/v2 options the `DefaultUnmarshaler` uses:
	// case-insensitive member names as in encoding/json v1 (with '_' and '-' kept
	// significant) and time.Duration from int64 nanoseconds. Duplicate object names
	// and invalid UTF-8 in the input are rejected, which is the v2 default.
	JSONUnmarshalOptions = json.JoinOptions(
		unmarshalDurationFromNanos,
		json.MatchCaseInsensitiveNames(true),
		jsonv1.MatchCaseSensitiveDelimiter(true),
	)
)

var (
	// DefaultMarshaler encodes message bodies for `Marshal`, `SendObject`, `AskObject`
	// and `ReplyObject` when the value is not a `MessageObjectMarshaler`.
	// It is encoding/json/v2 with `JSONMarshalOptions`. Reassign it for another format.
	DefaultMarshaler Marshaler = func(v any) ([]byte, error) {
		return json.Marshal(v, JSONMarshalOptions)
	}

	// DefaultUnmarshaler decodes message bodies for `Message.Unmarshal` and `Message.As`
	// when the target is not a `MessageObjectUnmarshaler`.
	// It is encoding/json/v2 with `JSONUnmarshalOptions`. Reassign it for another format.
	DefaultUnmarshaler Unmarshaler = func(body []byte, outPtr any) error {
		return json.Unmarshal(body, outPtr, JSONUnmarshalOptions)
	}
)
