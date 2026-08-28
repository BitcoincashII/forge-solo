package mining

import (
	"encoding/json"
	"strings"
	"testing"
)

// The validateaddress body must be marshalled, not interpolated.
//
// SetPoolAddress only rejects an empty address before this runs, so whatever is stored as the
// payout address reaches the RPC body verbatim. When the body was built with Sprintf, an
// address containing a quote could close the params array and append its own "method" key --
// and JSON decoders take the LAST of duplicate keys, so the node would have executed that one
// instead of validateaddress. The address is operator-supplied and the failure was closed
// (mining stays paused), but the shape should not exist.
func TestValidateAddressRequestCannotBeEscaped(t *testing.T) {
	hostile := []string{
		`x"],"method":"stop","params":["`,
		`x","method":"stop","id":"pkh`,
		`x\","method":"stop","params":["`,
		`bitcoincashii:qqq"}]}{"method":"stop"`,
	}
	for _, addr := range hostile {
		body, err := validateAddressRequest(addr)
		if err != nil {
			t.Fatalf("marshal %q: %v", addr, err)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body for %q is not valid JSON: %v (%s)", addr, err, body)
		}
		if got["method"] != "validateaddress" {
			t.Errorf("address %q changed the RPC method to %v", addr, got["method"])
		}
		params, ok := got["params"].([]interface{})
		if !ok || len(params) != 1 {
			t.Fatalf("address %q produced params %#v, want exactly one", addr, got["params"])
		}
		if params[0] != addr {
			t.Errorf("address round-tripped as %q, want %q", params[0], addr)
		}
		// Exactly one "method" key: a decoder taking the last duplicate must find only ours.
		if n := strings.Count(string(body), `"method"`); n != 1 {
			t.Errorf("body for %q contains %d method keys:\n%s", addr, n, body)
		}
	}
}

// The ordinary case must still produce the request the node expects.
func TestValidateAddressRequestShape(t *testing.T) {
	body, err := validateAddressRequest("bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		JSONRPC string   `json:"jsonrpc"`
		ID      string   `json:"id"`
		Method  string   `json:"method"`
		Params  []string `json:"params"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.JSONRPC != "1.0" || got.ID != "pkh" || got.Method != "validateaddress" {
		t.Errorf("wrong envelope: %+v", got)
	}
	if len(got.Params) != 1 || !strings.HasPrefix(got.Params[0], "bitcoincashii:") {
		t.Errorf("wrong params: %+v", got.Params)
	}
}
