package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A module that cannot start prints its error, and "core said 400" is not something an operator
// can act on. The reason is written into the body and was being dropped on the floor, which is
// why a stand that refused a module for half an hour had nothing in its logs but a number.
func TestARefusalIsReportedWithItsReason(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		wants  string
	}{
		{
			name: "the core's own words", status: 400,
			body:  `{"error":{"code":"bad_request","message":"this module says it cannot work without a setting"}}`,
			wants: "this module says it cannot work without a setting",
		},
		{
			name: "a body this code does not know", status: 502,
			body:  `<html>bad gateway</html>`,
			wants: "<html>bad gateway</html>",
		},
		{
			name: "nothing at all", status: 503,
			body:  "",
			wants: "said nothing",
		},
		{
			// An empty message with a body around it is not a reason, and printing the
			// wrapper instead of saying nothing useful would be worse than either.
			name: "a wrapper with no reason in it", status: 400,
			body:  `{"error":{"code":"bad_request","message":""}}`,
			wants: "bad_request",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: c.status,
				Body:       io.NopCloser(strings.NewReader(c.body)),
			}
			err := coreRefusal(response)
			if err == nil {
				t.Fatal("a refusal produced no error")
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Fatalf("the reason is missing from %q, wanted it to name %q", err, c.wants)
			}
		})
	}
}
