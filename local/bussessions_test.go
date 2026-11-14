package local

import (
	"encoding/json"
	"testing"
)

func TestBusSessions(t *testing.T) {
	var b busSessions
	get := func(flags map[string]bool) map[string]map[string]any {
		out := map[string]map[string]any{}
		for k, v := range b.state(flags) {
			var m map[string]any
			if err := json.Unmarshal(v, &m); err != nil {
				t.Fatal(err)
			}
			out[k] = m
		}
		return out
	}
	if s := get(nil); s["i2c"]["open"] != false || s["uart"]["open"] != false || s["can"]["open"] != false {
		t.Fatalf("nothing open: %v", s)
	}
	b.observe("i2c_open", json.RawMessage(`{"open":true,"sda":2,"scl":1,"hz":400000,"pullups":"pod"}`), true)
	b.observe("uart_open", nil, false) // a failed open
	s := get(nil)
	if s["i2c"]["open"] != true || s["i2c"]["hz"] != 400000.0 || s["uart"]["open"] != false {
		t.Fatalf("i2c open: %v", s)
	}
	// The MCP server says the I2C session is gone (it dropped it): closed, and forgotten.
	if s := get(map[string]bool{"i2c": false, "uart": false, "can": true}); s["i2c"]["open"] != false || s["can"]["open"] != true {
		t.Fatalf("status flags: %v", s)
	}
	if s := get(nil); s["i2c"]["open"] != false {
		t.Fatalf("a session status reported closed stays closed: %v", s)
	}
	b.observe("can_open", json.RawMessage(`{"open":true,"bitrate":500000}`), true)
	b.observe("can_close", nil, true)
	if s := get(nil); s["can"]["open"] != false {
		t.Fatalf("closed: %v", s)
	}
	b.observe("uart_open", json.RawMessage(`{"open":true}`), true)
	b.reset()
	if s := get(nil); s["uart"]["open"] != false {
		t.Fatalf("reset: %v", s)
	}
}
