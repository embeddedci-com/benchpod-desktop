package local

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/embeddedci-com/benchpod-desktop/agent"
)

// fakeConnect answers connect per connection ("" = the LAN default), as the MCP server would.
type fakeConnect map[string]agent.ToolResult

func (f fakeConnect) Call(_ context.Context, name string, args map[string]any) (agent.ToolResult, error) {
	c, _ := args["connection"].(string)
	if r, ok := f[c]; ok && name == "connect" {
		return r, nil
	}
	return agent.ToolResult{Text: "Error executing tool connect: TransportError: no BenchPod found", IsError: true}, nil
}

// The errors embeddedci-mcp 4.0.1 gave with two pods plugged in over USB (both also on their USB network links).
const (
	twoOnUSB = "Error executing tool connect: ConnectionConfigError: 2 pods on USB (benchpod-30a9f1 on /dev/cu.usbmodem345F389132331, " +
		"benchpod-c2f662 on /dev/cu.usbmodem3486389032331): pick one with connection usb:<name>, e.g. usb:benchpod-30a9f1"
	twoOnLAN = "Error executing tool connect: ConnectionConfigError: no connection configured and mDNS discovery did not settle on one pod: " +
		"2 BenchPods found — disambiguate with an address or hostname (e.g. benchpod-30a9f1.local):\n" +
		"  - BenchPod 30a9f1  10.237.134.169:8080  id=AbCdEfGhIjKl…\n" +
		"  - BenchPod c2f662  192.168.1.164:8080  id=MnOpQrStUvWx…\n" +
		"To connect, pass connection= one of: host[:port] (a pod on the LAN: TLS on 8443 by default), 'usb' ..."
)

func TestFindSeveralPodsOnUSB(t *testing.T) {
	a := &App{}
	f := a.tryConnect(context.Background(), fakeConnect{"usb": {Text: twoOnUSB, IsError: true}}, []string{"usb", ""})
	want := []podChoice{{"benchpod-30a9f1", "usb:benchpod-30a9f1"}, {"benchpod-c2f662", "usb:benchpod-c2f662"}}
	if f.Connected || !reflect.DeepEqual(f.Choices, want) {
		t.Fatalf("choices %+v, want %+v (found %+v)", f.Choices, want, f)
	}
	if !reflect.DeepEqual(f.Tried, []string{"usb", "your network"}) {
		t.Fatalf("tried %v", f.Tried)
	}
}

func TestFindSeveralPodsOnLAN(t *testing.T) {
	a := &App{}
	f := a.tryConnect(context.Background(), fakeConnect{"": {Text: twoOnLAN, IsError: true}}, []string{"usb", ""})
	want := []podChoice{{"benchpod-30a9f1", "benchpod-30a9f1.local"}, {"benchpod-c2f662", "benchpod-c2f662.local"}}
	if f.Connected || !reflect.DeepEqual(f.Choices, want) {
		t.Fatalf("choices %+v, want %+v", f.Choices, want)
	}
}

// Seen on USB and on the LAN: each pod once, over USB (tried first).
func TestFindSeveralPodsOnBoth(t *testing.T) {
	a := &App{}
	f := a.tryConnect(context.Background(), fakeConnect{
		"usb": {Text: twoOnUSB, IsError: true},
		"":    {Text: twoOnLAN, IsError: true},
	}, []string{"usb", ""})
	want := []podChoice{{"benchpod-30a9f1", "usb:benchpod-30a9f1"}, {"benchpod-c2f662", "usb:benchpod-c2f662"}}
	if !reflect.DeepEqual(f.Choices, want) {
		t.Fatalf("choices %+v, want %+v", f.Choices, want)
	}
}

func TestFindOnePodConnects(t *testing.T) {
	a := &App{}
	st, _ := json.Marshal(map[string]any{"kind": "usb_net", "la_voltage": 3.3,
		"firmware": map[string]any{"version": "4.0.0", "board_rev": "v3r2", "ip": "10.237.134.169"}})
	f := a.tryConnect(context.Background(), fakeConnect{"usb": {Text: "connected", Structured: st}}, []string{"usb", ""})
	if !f.Connected || f.Connection != "usb" || f.Version != "4.0.0" || f.Board != "v3r2" || f.Kind != "usb_net" || f.Choices != nil {
		t.Fatalf("found %+v", f)
	}
	if !a.pod.Connected || a.pod.Connection != "usb" {
		t.Fatalf("pod state %+v", a.pod)
	}
}

func TestSeveralPodsText(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want []podChoice
	}{
		{"TransportError: no BenchPod found among the USB serial ports probed (/dev/cu.usbmodem1)", nil},
		{"no BenchPod found on the local network via mDNS", nil},
		// Asked for one that is not there: offer the one that is.
		{"ConnectionConfigError: no pod named benchpod-000000 on USB (found: benchpod-30a9f1 on /dev/cu.usbmodem1)",
			[]podChoice{{"benchpod-30a9f1", "usb:benchpod-30a9f1"}}},
		{"no pod named benchpod-000000 on USB (found: none)", nil},
	} {
		if got := severalPods(tc.msg); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: %+v, want %+v", tc.msg, got, tc.want)
		}
	}
}
