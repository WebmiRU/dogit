package modulechan

import "encoding/json"

// Payload is the one place a payload becomes bytes.
//
// Encoding at the call site would mean every caller handles the error differently, and the one
// that forgets is the one that sends an empty body — which reads on the other end as a module
// that had nothing to say.
//
// Bytes and json.RawMessage are passed through rather than encoded, because encoding a []byte
// produces a base64 string inside a JSON string, and the far end is then asked to read an object
// and finds a string. That is not a shape it can report usefully: a module says "this is not a
// question I can read", which is a true sentence about a string and says nothing whatever about
// the caller who built a perfectly good object and handed over the finished bytes.
//
// It cost a deployment on this instance to work that out. The core built a question about a
// digest, handed over the JSON, and the registry module was sent a base64 string; the core then
// built an answer the same way and was sent a base64 string back. Both ends reported a shape
// problem, both shapes were correct, and in the middle every deployment was applied by a mutable
// tag — so the manifest never changed between runs, the cluster never rolled anything out, and
// the page reported that every pod was running the new image.
//
// Both forms are in use by callers on both sides and neither is wrong to pass what it passes.
// So the encoding is chosen by what it is given, once, here.
func Payload(payload any) (json.RawMessage, error) {
	switch already := payload.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return already, nil
	case []byte:
		return json.RawMessage(already), nil
	default:
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	}
}

// jsonRaw is the internal name Payload had when only this package used it.
func jsonRaw(payload any) (json.RawMessage, error) {
	return Payload(payload)
}
