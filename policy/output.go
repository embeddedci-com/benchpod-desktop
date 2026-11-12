package policy

import (
	"fmt"
	"math"
	"sync"
)

// Output follows which DAC output the pod has routed and the clamp of a running control loop, from
// the calls that ran. control_loop drives the output last routed in raw 16-bit codes, so its clamp
// can only be held to the limits in volts when the routed output is known.
//
// Observe each call after it ran (the agent's and the bench controls' alike). A call that failed
// or an unknown routing leaves the output unknown, and a control loop is then refused while a DAC
// limit is set: route the output first (dac_output with the path and volts 0).
type Output struct {
	mu   sync.Mutex
	path string // 3v3, 5v, 12v, current_out; "" = unknown or off
	loop *[2]int
}

// Path is the output last routed, "" when unknown.
func (o *Output) Path() string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.path
}

// Reset forgets the routing: a new connection, another pod.
func (o *Output) Reset() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.path, o.loop = "", nil
	o.mu.Unlock()
}

// Observe records a call that ran; ok is false when it failed (the routing is then unknown).
func (o *Output) Observe(name string, args map[string]any, ok bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	path, routes := routedBy(name, args)
	if routes {
		o.path = path
		if !ok {
			o.path = ""
		}
	}
	switch name {
	case "control_loop":
		o.loop = nil
		if ok {
			lo, hi := loopClamp(args)
			o.loop = &[2]int{lo, hi}
		}
	case "dac_stop", "dac_output", "generate", "replay", "replay_waveform", "current_out", "fpga_image":
		o.loop = nil
	}
}

// armedLoop is the clamp of the running loop, if any.
func (o *Output) armedLoop() (lo, hi int, ok bool) {
	if o == nil {
		return 0, 0, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.loop == nil {
		return 0, 0, false
	}
	return o.loop[0], o.loop[1], true
}

// routedBy says whether a call (re)routes the DAC and to which output ("" = off or unknown).
func routedBy(name string, args map[string]any) (string, bool) {
	str := func(k string) string { s, _ := args[k].(string); return s }
	switch name {
	case "dac_output":
		if p := str("path"); p != "off" {
			return p, true
		}
		return "", true
	case "generate", "replay":
		if r, ok := args["route"].(bool); ok && !r {
			return "", false // keeps the routing
		}
		if p := str("dac_path"); p != "" {
			return p, true
		}
		return "3v3", true
	case "replay_waveform":
		return str("dac_path"), true // "" = the waveform's own: unknown here
	case "current_out":
		if _, set := args["current"]; set {
			return "current_out", true
		}
	case "analog_path":
		switch str("path") {
		case "dac_3v3":
			return "3v3", true
		case "dac_5v":
			return "5v", true
		case "dac_12v":
			return "12v", true
		case "current_out":
			return "current_out", true
		}
		return "", true
	case "calibrate_dac", "calibrate", "calibrate_adc_gain", "command", "adc_range":
		return "", true
	}
	return "", false
}

func loopClamp(args map[string]any) (int, int) {
	lo, hi := 0, 65535
	if v, ok := num(args["vmin"]); ok {
		lo = int(v)
	}
	if v, ok := num(args["vmax"]); ok {
		hi = int(v)
	}
	return lo, hi
}

// pathSpan is the range an output spans in its own unit (nominal): volts, amps on current_out.
var pathSpan = map[string][2]float64{"3v3": {0, 3.3}, "5v": {0, 5}, "12v": {-12, 12}, "current_out": {0.004, 0.0201}}

// codeValue is what a 16-bit DAC code drives on an output.
func codeValue(code float64, path string) float64 {
	s, ok := pathSpan[path]
	if !ok {
		s = pathSpan["3v3"]
	}
	code = math.Max(0, math.Min(65535, code))
	return s[0] + code/65535*(s[1]-s[0])
}

// limitFor is the limit and unit that hold on an output.
func (l Limits) limitFor(path string) (float64, string) {
	if path == "current_out" {
		return l.MaxCurrentOut, "A"
	}
	return l.MaxDACVolts, "V"
}

// checkCodes holds a code range on an output to the limits.
func (l Limits) checkCodes(what string, lo, hi int, path string) error {
	limit, unit := l.limitFor(path)
	if limit <= 0 {
		return nil
	}
	// Half a code of slack: a limit typed in volts rounds to the nearest code.
	s := pathSpan[path]
	if s == ([2]float64{}) {
		s = pathSpan["3v3"]
	}
	slack := (s[1] - s[0]) / 65535 / 2
	for _, c := range []int{lo, hi} {
		if v := codeValue(float64(c), path); math.Abs(v) > limit+slack {
			return fmt.Errorf("%s reaches %.3g %s on %s, above this bench's limit of %.3g %s", what, v, unit, path, limit, unit)
		}
	}
	return nil
}

// checkLoop holds control_loop's clamp (vmin..vmax, codes on the routed output) to the limits.
func (l Limits) checkLoop(o *Output, args map[string]any) error {
	if o == nil {
		return nil
	}
	path := o.Path()
	if path == "" {
		if l.MaxDACVolts <= 0 && l.MaxCurrentOut <= 0 {
			return nil
		}
		return fmt.Errorf("the control loop drives the output last routed, and which one that is is not known here: route it first with dac_output (path and volts 0), then arm the loop")
	}
	lo, hi := loopClamp(args)
	return l.checkCodes("the loop's clamp (vmin..vmax)", lo, hi, path)
}

// checkReroute refuses moving a running loop onto an output where its clamp is above the limits.
func (l Limits) checkReroute(o *Output, name string, args map[string]any) error {
	lo, hi, armed := o.armedLoop()
	if !armed || name != "analog_path" {
		return nil
	}
	path, _ := routedBy(name, args)
	if path == "" {
		return nil
	}
	return l.checkCodes("the running control loop's clamp", lo, hi, path)
}
