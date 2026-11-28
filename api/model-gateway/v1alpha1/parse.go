package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ParseTokenRequest reads body as T-3 has it: exactly one object, whose members are TokenRequest's,
// each once and named case-sensitively, each a string, and nothing after it. It does not Check the
// request.
func ParseTokenRequest(body []byte) (TokenRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return TokenRequest{}, errors.New("the body is no JSON object")
	}
	var request TokenRequest
	members := map[string]*string{"namespace": &request.Namespace, "name": &request.Name, "uid": &request.UID}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return TokenRequest{}, errors.New("the body is no JSON object")
		}
		into, known := members[name]
		switch {
		case !known:
			return TokenRequest{}, fmt.Errorf("the member %q is not TokenRequest's", name)
		case seen[name]:
			return TokenRequest{}, fmt.Errorf("the member %q is named twice", name)
		}
		seen[name] = true
		if err := decoder.Decode(into); err != nil {
			return TokenRequest{}, fmt.Errorf("the member %q is no string", name)
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return TokenRequest{}, errors.New("the body is no JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return TokenRequest{}, errors.New("the body carries something after its object")
	}
	return request, nil
}

// BearerToken is the token of an Authorization header's value, whose scheme is Bearer in any case
// (T-4), or "" where it carries none.
func BearerToken(authorization string) string {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimLeft(token, " ")
}
