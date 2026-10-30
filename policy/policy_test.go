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
	d := Desktop(Limits{}, true)
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
