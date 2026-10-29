// Package policy holds the agent policies for the BenchPod tools: which tools each host shows
// the model, which need the user's yes, and the hardware limits checked on every call.
//
// The limits are enforced here, in the tool layer the model cannot talk its way past, never
// only in the prompt (see the design doc, section 7). Tool names are the BenchPod MCP server's
// (embeddedci-mcp); a tool this package does not know is treated as risky.
package policy

import (
	"fmt"
	"math"

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
	"loop_input", "set_pull", "set_la_voltage", "gpio_write", "gpio_pulse", "gpio_mode",
	"set_wiring", "fpga_image", "calibrate", "calibrate_dac", "calibrate_adc_gain",
	"lan_tls", "usb_network", "command",
	"can_write", "can_respond", "spi_transfer",
}

// ReadOnly are tools that only observe. They never need a confirmation.
var ReadOnly = []string{
	"status", "wiring", "power_status", "pull_status", "la_pins", "la_timing", "calibration",
	"analog_path", "list_waveforms", "adc_read", "gpio_read", "gpio_wait",
	"capture_la", "capture_adc", "capture_uart", "capture_correlated", "decode_la",
	"uart_read", "can_read", "can_status", "measure_power",
	"i2c_sensor_status", "i2c_sensor_types", "i2c_sensor_regs", "i2c_sensor_capture",
	"gps_status", "spi_flash_info", "spi_flash_read", "loop_probe", "cloud_list_devices",
}

// Check returns an agent.Policy Check func enforcing the limits.
func (l Limits) Check() func(name string, args map[string]any) error {
	return func(name string, args map[string]any) error {
		switch name {
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
		}
		return nil
	}
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
// confirm is false), connect and disconnect left to the app (it owns the connection).
func Desktop(l Limits, confirm bool) agent.Policy {
	p := agent.Policy{
		Deny:         []string{"connect", "disconnect"},
		Check:        l.Check(),
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
func Pod(l Limits) agent.Policy {
	return agent.Policy{
		Deny: []string{
			"connect", "disconnect", "cloud_list_devices",
			"flash", "spi_flash_program", // their file argument is a path on the machine running the MCP server
			"save_capture_as_recording", "lan_tls", "usb_network", "command",
		},
		Confirm:         Risky,
		Check:           l.Check(),
		MaxToolCalls:    30,
		MaxTurns:        36,
		MaxOutputTokens: 4000,
	}
}
