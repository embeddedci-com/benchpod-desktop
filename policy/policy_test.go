package policy

import "testing"

func TestLimits(t *testing.T) {
	check := Limits{MaxDACVolts: 3.3, MaxCurrentOut: 0.02}.Check()
	cases := []struct {
		tool string
		args map[string]any
		ok   bool
	}{
		{"dac_output", map[string]any{"volts": 3.0}, true},
		{"dac_output", map[string]any{"volts": 5.0}, false},
		{"dac_output", map[string]any{"volts": -4.0}, false},
		{"dac_output", map[string]any{}, true}, // only routes
		{"generate", map[string]any{"amplitude": 1.0, "offset": 1.5}, true},
		{"generate", map[string]any{"amplitude": 2.0, "offset": 1.5}, false},
		{"generate", map[string]any{"dac_path": "current_out", "amplitude": 0.004, "offset": 0.012}, true},
		{"generate", map[string]any{"dac_path": "current_out", "amplitude": 0.01, "offset": 0.015}, false},
		{"current_out", map[string]any{"current": 0.024}, false},
		{"status", map[string]any{"volts": 99.0}, true},
		{"replay", map[string]any{"volts": []any{0.0, 1.0, 3.0}}, true},
		{"replay", map[string]any{"volts": []any{0.0, -4.0}, "dac_path": "12v"}, false},
		{"replay", map[string]any{"volts": []any{0.0, 1.0}, "mapping": "fit", "dac_path": "5v"}, false},
		{"replay", map[string]any{"volts": []any{0.0, 1.0}, "mapping": "fit"}, true},
		{"replay", map[string]any{"volts": []any{0.004, 0.019}, "dac_path": "current_out"}, true},
		{"replay", map[string]any{"from_last_capture": true}, true},
		{"replay_waveform", map[string]any{"waveform_id": "x", "mapping": "fit", "dac_path": "12v"}, false},
	}
	for _, c := range cases {
		if err := check(c.tool, c.args); (err == nil) != c.ok {
			t.Errorf("%s %v: err=%v, want ok=%v", c.tool, c.args, err, c.ok)
		}
	}
	if err := (Limits{}).Check()("dac_output", map[string]any{"volts": 11.0}); err != nil {
		t.Errorf("zero limits must not refuse: %v", err)
	}
}

func TestPoliciesHideConnectionTools(t *testing.T) {
	d := Desktop(Limits{}, true, nil)
	for _, n := range []string{"connect", "disconnect"} {
		if contains(d.Deny, n) == false {
			t.Errorf("desktop offers %s", n)
		}
	}
	demo := Demo()
	for _, n := range []string{"connect", "flash", "spi_flash_program", "command", "set_wiring", "save_capture_as_recording"} {
		if contains(demo.Allow, n) {
			t.Errorf("demo offers %s", n)
		}
	}
	pod := Pod(Limits{})
	for _, n := range []string{"connect", "spi_flash_program", "command", "lan_tls"} {
		if !contains(pod.Deny, n) {
			t.Errorf("AI tab offers %s", n)
		}
	}
	if contains(pod.Deny, "flash") || !contains(pod.Confirm, "flash") {
		t.Error("AI tab: flash must be offered, with a confirmation")
	}
	for name, args := range map[string]map[string]any{
		"a file":    {"file": "/etc/passwd", "build": "b1"},
		"local run": {"build": "b1", "server_side": false},
		"no build":  {},
	} {
		if pod.Check("flash", args) == nil {
			t.Errorf("AI tab flashes with %s", name)
		}
	}
	withFile := PodFiles(Limits{}, func(f string) bool { return f == "app.elf" })
	if err := withFile.Check("flash", map[string]any{"file": "app.elf"}); err != nil {
		t.Errorf("vouched file refused: %v", err)
	}
	if withFile.Check("flash", map[string]any{"file": "/etc/passwd"}) == nil {
		t.Error("an unvouched path was allowed")
	}
	for _, args := range []map[string]any{{"build": "b1"}, {"build": "latest", "repo": "o/r", "ref": "main"}} {
		if err := pod.Check("flash", args); err != nil {
			t.Errorf("AI tab refuses %v: %v", args, err)
		}
	}
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func TestOfflineRefusesCloud(t *testing.T) {
	p := Offline(Desktop(Limits{}, true, nil))
	for _, name := range OfflineDeny {
		found := false
		for _, d := range p.Deny {
			found = found || d == name
		}
		if !found {
			t.Errorf("%s not denied offline", name)
		}
	}
	if err := p.Check("flash", map[string]any{"build": "latest"}); err == nil {
		t.Error("flash of an uploaded build allowed offline")
	}
	if err := p.Check("flash", map[string]any{"file": "fw.elf"}); err != nil {
		t.Errorf("local flash refused offline: %v", err)
	}
	if err := p.Check("vision_connect", map[string]any{"connection": "embeddedci:cam"}); err == nil {
		t.Error("cloud vision board allowed offline")
	}
	if err := p.Check("dac_output", map[string]any{"volts": 1.0}); err != nil {
		t.Errorf("dac_output refused offline: %v", err)
	}
}

func TestFaultLevel(t *testing.T) {
	check := Limits{MaxDACVolts: 5, MaxCurrentOut: 0.02}.Check()
	cases := []struct {
		args map[string]any
		ok   bool
	}{
		// 3v3 tops out below 5 V: any level is fine.
		{map[string]any{"volts": []any{1.0}, "fault": map[string]any{"type": "spike"}}, true},
		// code 65535 on 12v is +12 V; code 0 is -12 V; mid-scale is 0 V.
		{map[string]any{"volts": []any{1.0}, "dac_path": "12v", "fault": map[string]any{"type": "spike"}}, false},
		{map[string]any{"volts": []any{1.0}, "dac_path": "12v", "fault": map[string]any{"type": "flatline"}}, false},
		{map[string]any{"volts": []any{1.0}, "dac_path": "12v", "fault": map[string]any{"type": "flatline", "level": 32768.0}}, true},
		{map[string]any{"volts": []any{1.0}, "dac_path": "12v", "fault": map[string]any{"type": "stuck", "level": 50000.0}}, false},
		// current_out tops out at 20.1 mA, above a 20 mA limit.
		{map[string]any{"volts": []any{0.01}, "dac_path": "current_out", "fault": map[string]any{"type": "spike"}}, false},
		{map[string]any{"volts": []any{0.01}, "dac_path": "current_out", "fault": map[string]any{"type": "spike", "level": 30000.0}}, true},
		{map[string]any{"waveform_id": "x", "dac_path": "12v", "fault": map[string]any{"type": "spike", "level": 40000.0}}, true},
	}
	for _, c := range cases {
		if err := check("replay", c.args); (err == nil) != c.ok {
			t.Errorf("%v: err=%v, want ok=%v", c.args, err, c.ok)
		}
	}
}

func TestControlLoopClamp(t *testing.T) {
	var out Output
	l := Limits{MaxDACVolts: 5}
	check := l.CheckOutput(&out)
	loop := func(vmin, vmax float64) map[string]any { return map[string]any{"vmin": vmin, "vmax": vmax} }

	// Nothing routed yet: which output the codes drive is not known.
	if err := check("control_loop", loop(0, 65535)); err == nil {
		t.Error("loop with an unknown output must be refused under a DAC limit")
	}
	if err := (Limits{}).CheckOutput(&out)("control_loop", loop(0, 65535)); err != nil {
		t.Errorf("no limits, no refusal: %v", err)
	}
	out.Observe("dac_output", map[string]any{"path": "3v3", "volts": 0.0}, true)
	if err := check("control_loop", loop(0, 65535)); err != nil {
		t.Errorf("3v3 full range is under 5 V: %v", err)
	}
	out.Observe("dac_output", map[string]any{"path": "12v", "volts": 0.0}, true)
	if err := check("control_loop", loop(0, 65535)); err == nil {
		t.Error("12v full range (±12 V) must be refused under 5 V")
	}
	// 0..5 V on 12v: codes 32768..46421.
	if err := check("control_loop", loop(32768, 46421)); err != nil {
		t.Errorf("0..5 V on 12v: %v", err)
	}
	if err := check("control_loop", loop(32768, 50000)); err == nil {
		t.Error("vmax above 5 V on 12v must be refused")
	}
	// A failed routing leaves it unknown.
	out.Observe("dac_output", map[string]any{"path": "3v3"}, false)
	if out.Path() != "" {
		t.Errorf("failed routing: path %q, want unknown", out.Path())
	}
	// generate and replay route their dac_path unless route is false.
	out.Observe("generate", map[string]any{"dac_path": "5v"}, true)
	if out.Path() != "5v" {
		t.Errorf("generate: %q", out.Path())
	}
	out.Observe("replay", map[string]any{"dac_path": "12v", "route": false}, true)
	if out.Path() != "5v" {
		t.Errorf("replay route=false must keep the routing: %q", out.Path())
	}

	// A running loop may not be moved onto an output where its clamp is above the limit.
	out.Observe("dac_output", map[string]any{"path": "3v3", "volts": 0.0}, true)
	out.Observe("control_loop", loop(0, 65535), true)
	if err := check("analog_path", map[string]any{"path": "dac_12v"}); err == nil {
		t.Error("moving a full-range loop onto 12v must be refused")
	}
	if err := check("analog_path", map[string]any{"path": "cal2"}); err == nil {
		t.Error("cal2 is the 12v output: must be refused")
	}
	if err := check("adc_read", map[string]any{"source": "cal2"}); err == nil {
		t.Error("reading the cal2 loopback routes the 12v output: must be refused")
	}
	if err := check("adc_read", map[string]any{"source": "ext"}); err != nil {
		t.Errorf("an ext read leaves the DAC: %v", err)
	}
	if err := check("analog_path", map[string]any{"path": "cal1"}); err != nil {
		t.Errorf("cal1 is the 5v output: %v", err)
	}
	if err := check("analog_path", map[string]any{"path": "dac_5v"}); err != nil {
		t.Errorf("onto 5v is within 5 V: %v", err)
	}
	out.Observe("dac_stop", map[string]any{}, true)
	if err := check("analog_path", map[string]any{"path": "dac_12v"}); err != nil {
		t.Errorf("no loop running: %v", err)
	}
	out.Reset()
	if out.Path() != "" {
		t.Error("Reset must forget the routing")
	}
}

func TestLoopInputNeedsNoConfirm(t *testing.T) {
	d := Desktop(Limits{}, true, nil)
	if d.NeedsConfirm("loop_input") {
		t.Error("loop_input only moves an armed loop's input: no confirmation")
	}
	if !d.NeedsConfirm("control_loop") || !d.NeedsConfirm("analog_path") {
		t.Error("control_loop and analog_path change the output: confirm them")
	}
}
