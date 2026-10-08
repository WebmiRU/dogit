package modulechan

import "encoding/json"

// jsonRaw is the one place a payload becomes bytes.
//
// Encoding at the call site would mean every caller handles the error differently, and the one
// that forgets is the one that sends an empty body — which reads on the other end as a module
// that had nothing to say.
func jsonRaw(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
