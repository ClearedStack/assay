// Package errs holds the three public error codes and the only error shape
// Assay ever shows outside the process.
//
// An Error is built from fixed words chosen in this repository: a code, a
// reason and, sometimes, the name of a rule or a declared fact. Text taken
// from the input (a field name nobody declared, an unfamiliar value) is never
// copied into it. Such text only feeds the fingerprint, a one-way hash, so a
// machine name, an address or a credential cannot leak through an error.
package errs

import (
	"crypto/sha256"
	"encoding/hex"
)

// Code is one of the three public error codes.
type Code string

const (
	// Timeout: something outside took too long.
	Timeout Code = "E_TIMEOUT"
	// Parse: the data was broken.
	Parse Code = "E_PARSE"
	// NeedsSubtool: the data was valid and unfamiliar.
	NeedsSubtool Code = "E_NEEDS_SUBTOOL"
)

// Reason says, in a fixed word, what went wrong. The set is closed.
type Reason string

// Reasons for E_PARSE.
const (
	TooLarge            Reason = "too_large"
	MalformedJSON       Reason = "malformed_json"
	DuplicateKey        Reason = "duplicate_key"
	NotAnObject         Reason = "not_an_object"
	MissingField        Reason = "missing_field"
	InvalidType         Reason = "invalid_type"
	InvalidValue        Reason = "invalid_value"
	UnsupportedContract Reason = "unsupported_contract"
	MissingFact         Reason = "missing_fact"
	OutOfRange          Reason = "out_of_range"
	HubReplyInvalid     Reason = "hub_reply_invalid"
	HubRejectedRequest  Reason = "hub_rejected_request"
)

// Reasons for E_NEEDS_SUBTOOL.
const (
	UnknownField    Reason = "unknown_field"
	UnknownCategory Reason = "unknown_category"
	UnknownFact     Reason = "unknown_fact"
	UnknownValue    Reason = "unknown_value"
	NoMatchingRule  Reason = "no_matching_rule"
)

// Reasons for E_TIMEOUT.
const (
	HubTimeout     Reason = "hub_timeout"
	HubUnavailable Reason = "hub_unavailable"
)

// Error is the public error. Every field is safe to publish.
type Error struct {
	Code   Code   `json:"code"`
	Reason Reason `json:"reason"`
	// Where names the envelope field, declared fact, gate or factor the
	// error is about. It always comes from this repository's own rules.
	Where string `json:"where,omitempty"`
	// Fingerprint identifies this kind of failure so that reports can be
	// deduplicated. It is a truncated SHA-256 and cannot be reversed.
	Fingerprint string `json:"fingerprint"`
}

// New builds an Error. The scope values (for example the category, or the
// unfamiliar text itself) are mixed into the fingerprint and kept nowhere
// else.
func New(code Code, reason Reason, where string, scope ...string) *Error {
	h := sha256.New()
	for _, part := range append([]string{string(code), string(reason), where}, scope...) {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return &Error{
		Code:        code,
		Reason:      reason,
		Where:       where,
		Fingerprint: hex.EncodeToString(h.Sum(nil))[:16],
	}
}

// Error implements the error interface with the same safe words.
func (e *Error) Error() string {
	s := string(e.Code) + ": " + string(e.Reason)
	if e.Where != "" {
		s += " (" + e.Where + ")"
	}
	return s
}
