package api

import (
	"encoding/json"
	"io"
	"strings"
)

// decodeStrictBody decodes one JSON document into v and refuses a key v does not
// have, so a mistyped field on a money-path body fails loudly instead of being
// silently ignored: a dropped optional field changes what the request does. A
// trailing second document is refused too, as json.Unmarshal would. Only the
// unknown-key case says why; any other failure is the plain "invalid request
// body".
func decodeStrictBody(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return NewClientError(400, "invalid request body: "+err.Error())
		}
		return NewClientError(400, "invalid request body")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return NewClientError(400, "invalid request body")
	}
	return nil
}
