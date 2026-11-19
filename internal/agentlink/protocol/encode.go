package protocol

import (
	"encoding/json"
	"fmt"
)

// Messages this endpoint composes are built through the constructors below
// rather than by marshalling a free-form map, so every outgoing message
// carries the four routing members, exactly the body its kind defines, and
// nothing else.

type wireEnvelope struct {
	V      int64             `json:"v"`
	Kind   Kind              `json:"kind"`
	Origin Origin            `json:"origin"`
	ID     string            `json:"id"`
	Method string            `json:"method,omitempty"`
	Params json.RawMessage   `json:"params,omitempty"`
	Trace  map[string]string `json:"trace,omitempty"`

	Payload json.RawMessage `json:"payload,omitempty"`

	Status *wireStatus `json:"status,omitempty"`

	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type wireStatus struct {
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

func marshal(e *wireEnvelope) ([]byte, error) {
	msg, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("encoding %s message: %w", e.Kind, err)
	}
	if err := ValidateComposed(msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeRequest builds a request message. params is required and may be an
// empty object, but never absent: this profile has no zero value standing in
// for "unset".
func EncodeRequest(origin Origin, id, method string, params json.RawMessage, trace map[string]string) ([]byte, error) {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if len(method) > MaxMethodLen {
		return nil, fmt.Errorf("method exceeds %d bytes", MaxMethodLen)
	}
	if id == "" || len(id) > MaxIDLen {
		return nil, fmt.Errorf("id must be non-empty and at most %d bytes", MaxIDLen)
	}
	return marshal(&wireEnvelope{
		V: Version, Kind: KindRequest, Origin: origin, ID: id,
		Method: method, Params: params, Trace: trace,
	})
}

// EncodeData builds a data message carrying one chunk of a method's
// streaming payload.
func EncodeData(origin Origin, id string, payload json.RawMessage) ([]byte, error) {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	return marshal(&wireEnvelope{
		V: Version, Kind: KindData, Origin: origin, ID: id, Payload: payload,
	})
}

// EncodeResult builds the terminal result for one request.
//
// A result carries no content: data and result are distinct kinds, so a
// failing result can never arrive fused to payload bytes and a pre-header
// error stays unambiguous. The message is truncated to its bound here rather
// than by the caller, so a diagnostic naming rejected input can never grow an
// outgoing message past the composing bound.
func EncodeResult(origin Origin, id, reason, message string) ([]byte, error) {
	if !IsValidReason(reason) {
		return nil, fmt.Errorf("refusing to send unrecognised status reason %q", truncate(reason, MaxReasonLen))
	}
	return marshal(&wireEnvelope{
		V: Version, Kind: KindResult, Origin: origin, ID: id,
		Status: &wireStatus{Reason: reason, Message: truncate(message, MaxStatusMessageLen)},
	})
}

// EncodeCancel builds a cancel for one request. Cancellation is per request
// and never touches the link: a cancel must not close the connection and must
// not disturb any sibling.
func EncodeCancel(origin Origin, id, reason, message string) ([]byte, error) {
	return marshal(&wireEnvelope{
		V: Version, Kind: KindCancel, Origin: origin, ID: id,
		Reason: truncate(reason, MaxReasonLen), Message: truncate(message, MaxStatusMessageLen),
	})
}
