// Package policy holds the agent policies for the BenchPod tools: which tools each host shows
// the model, which need the user's yes, and the hardware limits checked on every call.
//
// The limits are enforced here, in the tool layer the model cannot talk its way past, never
// only in the prompt (see the design doc, section 7). Tool names are the BenchPod MCP server's
// (embeddedci-mcp); a tool this package does not know is treated as risky.
package policy

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/embeddedci-com/benchpod-desktop/agent"
)

// Limits are the hardware ceilings a call may not exceed. Zero means "no extra limit" (the pod's
// own range still applies).
type Limits struct {
	MaxDACVolts   float64 `json:"max_dac_volts"`   // dac_output volts, generate peak (amplitude + |offset|)
	MaxCurrentOut float64 `json:"max_current_out"` // current_out amps (the 4-20 mA loop)
}

// Risky are the tools that change what the DUT sees: power, firmware, outputs, pod settings.
// The desktop app asks before each of them unless the user turned confirmations off.
var Risky = []string{
	"power_on", "power_off", "power_cycle_and_capture", "reset_target",
	"flash", "spi_flash_program",
	"dac_output", "current_out", "generate", "replay", "replay_waveform", "control_loop",
	"analog_path", "set_pull", "set_la_voltage", "gpio_write", "gpio_pulse", "gpio_mode",
	"set_wiring", "fpga_image", "calibrate", "calibrate_dac", "calibrate_adc_gain",
	"lan_tls", "usb_network", "command",
	"can_write", "can_respond", "spi_transfer",
}

// ReadOnly are tools that only observe. They never need a confirmation.
var ReadOnly = []string{
	"status", "wiring", "power_status", "pull_status", "la_pins", "la_timing", "calibration",
	"list_waveforms", "adc_read", "gpio_read", "gpio_wait",
	"capture_la", "capture_adc", "capture_uart", "capture_correlated", "decode_la",
	"uart_read", "can_read", "can_status", "measure_power",
	"sensor_status", "sensor_regs", "sensor_capture", "sensor_library", "sensor_show",
	"sensor_format", "sensor_validate", "spi_flash_info", "spi_flash_read", "loop_probe", "cloud_list_devices",
}

// Check returns an agent.Policy Check func enforcing the limits. It has no view of the routed
// output, so it leaves the control loop's clamp alone: CheckOutput holds that one too.
func (l Limits) Check() func(name string, args map[string]any) error { return l.CheckOutput(nil) }

// CheckOutput is Check, and with o (the routed output, see Output) it also holds control_loop's
// clamp to the limits, refuses it while the routed output is unknown, and refuses analog_path (or
// an ADC read of the cal1/cal2 loopback) moving a running loop onto an output where its clamp is
// above them.
//
// loop_input needs no check and no confirmation: it only moves the input of a loop that is
// armed, whose output stays inside the clamp control_loop was checked with.
func (l Limits) CheckOutput(o *Output) func(name string, args map[string]any) error {
	return func(name string, args map[string]any) error {
		switch name {
		case "control_loop":
			return l.checkLoop(o, args)
		case "analog_path", "adc_read", "capture_adc", "capture_correlated":
			return l.checkReroute(o, name, args)
		case "dac_output":
			if v, ok := num(args["volts"]); ok && l.MaxDACVolts > 0 && math.Abs(v) > l.MaxDACVolts {
				return fmt.Errorf("%.3g V is above this bench's DAC limit of %.3g V", v, l.MaxDACVolts)
			}
		case "generate":
			if args["dac_path"] == "current_out" {
				amp, _ := num(args["amplitude"])
				off, _ := num(args["offset"])
				if l.MaxCurrentOut > 0 && math.Abs(amp)+math.Abs(off) > l.MaxCurrentOut {
					return fmt.Errorf("peak %.3g A is above this bench's current-loop limit of %.3g A", math.Abs(amp)+math.Abs(off), l.MaxCurrentOut)
				}
				return nil
			}
			amp, _ := num(args["amplitude"])
			off, _ := num(args["offset"])
			if l.MaxDACVolts > 0 && math.Abs(amp)+math.Abs(off) > l.MaxDACVolts {
				return fmt.Errorf("peak %.3g V (amplitude + offset) is above this bench's DAC limit of %.3g V", math.Abs(amp)+math.Abs(off), l.MaxDACVolts)
			}
		case "current_out":
			if v, ok := num(args["current"]); ok && l.MaxCurrentOut > 0 && math.Abs(v) > l.MaxCurrentOut {
				return fmt.Errorf("%.3g A is above this bench's current-loop limit of %.3g A", v, l.MaxCurrentOut)
			}
		case "replay", "replay_waveform":
			return l.checkReplay(args)
		}
		return nil
	}
}

// pathPeak is the most an output path can drive: what a replay scaled to fit it reaches.
var pathPeak = map[string]float64{"3v3": 3.3, "5v": 5, "12v": 12, "current_out": 0.0201}

// checkReplay holds a replay to the limits: its peak sample when it plays the values as given, or
// the output's full range when it is scaled to fit it.
func (l Limits) checkReplay(args map[string]any) error {
	path, _ := args["dac_path"].(string)
	if path == "" {
		path = "3v3"
	}
	limit, unit := l.MaxDACVolts, "V"
	if path == "current_out" {
		limit, unit = l.MaxCurrentOut, "A"
	}
	if limit <= 0 {
		return nil
	}
	if err := l.checkFault(args, path); err != nil {
		return err
	}
	if m, _ := args["mapping"].(string); m == "fit" {
		if p := pathPeak[path]; p > limit {
			return fmt.Errorf("a replay scaled to fit %s reaches %.3g %s, above this bench's limit of %.3g %s: play it as recorded (mapping faithful) or on a lower output", path, p, unit, limit, unit)
		}
		return nil
	}
	values, _ := args["volts"].([]any)
	peak := 0.0
	for _, v := range values {
		if f, ok := num(v); ok && math.Abs(f) > peak {
			peak = math.Abs(f)
		}
	}
	if peak > limit {
		return fmt.Errorf("the waveform peaks at %.3g %s, above this bench's limit of %.3g %s", peak, unit, limit, unit)
	}
	return nil
}

// checkFault holds a replay's fault level (a raw 16-bit DAC code) to the limits: the value it holds
// on the output. Without a level a flatline holds code 0 and a spike or stuck full scale.
func (l Limits) checkFault(args map[string]any, path string) error {
	f, ok := args["fault"].(map[string]any)
	if !ok {
		return nil
	}
	level, set := num(f["level"])
	if !set {
		level = 65535
		if t, _ := f["type"].(string); t == "flatline" {
			level = 0
		}
	}
	return l.checkCodes("the fault level", int(level), int(level), path)
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// Desktop is the community app's policy: every tool, the risky ones confirmed (unless
// confirm is false), connect and disconnect left to the app (it owns the connection). out is the
// routed output the app observes calls into (nil: the control loop's clamp is not checked).
func Desktop(l Limits, confirm bool, out *Output) agent.Policy {
	p := agent.Policy{
		Deny:         []string{"connect", "disconnect"},
		Check:        l.CheckOutput(out),
		MaxToolCalls: 40,
		MaxTurns:     50,
	}
	if confirm {
		p.Confirm = Risky
	}
	return p
}

// Demo is the public website demo against a simulated pod. Only the tools the simulator
// answers, nothing that reads a file or picks a connection, tight caps.
func Demo() agent.Policy {
	return agent.Policy{
		Allow: []string{
			"status", "wiring", "power_status", "power_on", "power_off", "power_cycle_and_capture", "reset_target",
			"uart_open", "uart_read", "uart_write", "uart_close", "capture_uart",
			"capture_la", "decode_la", "la_pins",
			"adc_read", "capture_adc", "dac_output", "dac_stop", "generate", "measure_power",
			"enable_i2c_sensor", "set_i2c_sensor", "disable_i2c_sensor", "i2c_sensor_status", "i2c_sensor_types",
			"i2c_sensor_capture", "i2c_sensor_regs", "set_pull", "pull_status",
			"gpio_read",
		},
		Check:           Limits{MaxDACVolts: 3.3}.Check(),
		MaxToolCalls:    16,
		MaxTurns:        18,
		MaxOutputTokens: 1200,
		MaxResultChars:  6000,
	}
}

// Pod is the AI tab of the EmbeddedCI server, driving the user's own pod: the desktop set
// minus the tools that read files on the server or reconfigure the pod's network.
func Pod(l Limits) agent.Policy { return PodFiles(l, nil) }

// PodFiles is Pod with files the host vouches for: flash may take a file allowed returns true for.
func PodFiles(l Limits, allowed func(string) bool) agent.Policy {
	return agent.Policy{
		Deny: []string{
			"connect", "disconnect", "cloud_list_devices",
			"spi_flash_program", // its file argument is a path on the machine running the MCP server
			"save_capture_as_recording", "lan_tls", "usb_network", "command",
		},
		Confirm:         Risky,
		Check:           podCheck(l, allowed),
		MaxToolCalls:    30,
		MaxTurns:        36,
		MaxOutputTokens: 4000,
	}
}

// podCheck adds to the limits: on a hosted server, flash takes a build uploaded to embeddedci.com,
// flashed by the server, or a file the host vouches for. Any other file is a path on the machine
// running the MCP server, and server_side=false on a build runs OpenOCD there.
func podCheck(l Limits, allowed func(string) bool) func(string, map[string]any) error {
	check := l.Check()
	return func(name string, args map[string]any) error {
		if name == "flash" {
			if f, _ := args["file"].(string); f != "" {
				if allowed == nil || !allowed(f) {
					return errors.New("flash takes an attached file or an uploaded build here, not a path: ask the user to attach the file, or give a build id or repo and ref")
				}
				return check(name, args)
			}
			if v, ok := args["server_side"].(bool); ok && !v {
				return errors.New("flash runs on the server here: server_side=false is not available")
			}
			b, _ := args["build"].(string)
			r, _ := args["repo"].(string)
			if b == "" && r == "" {
				return errors.New("flash needs an attached file or an uploaded build: a build id, or repo and ref")
			}
		}
		return check(name, args)
	}
}

// OfflineDeny are the tools that only work through embeddedci.com.
var OfflineDeny = []string{"cloud_list_devices", "vision_cloud_boards"}

// Offline narrows p for offline mode: no tool reaches embeddedci.com. The cloud listings are
// hidden, and a flash of an uploaded build or a connection to an 'embeddedci:' device is refused.
func Offline(p agent.Policy) agent.Policy {
	p.Deny = append(append([]string{}, p.Deny...), OfflineDeny...)
	inner := p.Check
	p.Check = func(name string, args map[string]any) error {
		if name == "flash" {
			for _, k := range []string{"build", "repo", "ref", "artifact"} {
				if v, _ := args[k].(string); v != "" {
					return errors.New("offline mode: flashing a build from embeddedci.com is turned off; flash a local file instead")
				}
			}
		}
		for _, v := range args {
			if s, ok := v.(string); ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "embeddedci:") {
				return errors.New("offline mode: devices on embeddedci.com are turned off")
			}
		}
		if inner != nil {
			return inner(name, args)
		}
		return nil
	}
	return p
}
