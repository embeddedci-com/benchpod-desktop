package local

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// The app drives pods on firmware 4.0 (client API 2): its MCP server (embeddedci-mcp 4) and its
// bundled benchpod CLI (0.10) refuse older firmware. The app tells these refusals apart by their
// kind, never by their wording: the MCP server's error class (the "FirmwareTooOldError:" or
// "ClientTooOldError:" it puts first) and the CLI's exit code. A pod on firmware 3.x still takes
// `benchpod flash-self`, so the app offers that update. Firmware 4.0.1 adds LAN pairing: over
// Ethernet and Wi-Fi a computer that is not paired with the pod gets its status only, and
// everything else is refused with BP0081 (lan_not_paired); the app then offers to pair it over USB
// or embeddedci.com (podpair.go).

// podCompat is why the app cannot drive the pod it reached, "" when it can.
type podCompat string

const (
	compatOK podCompat = ""
	// The pod runs firmware 3.x: update the pod (Settings, Pod firmware).
	compatFirmwareTooOld podCompat = "firmware_too_old"
	// The pod's firmware is newer than the MCP server or the CLI the app runs: update the app (or
	// the embeddedci-mcp on PATH, which the app runs instead of its pinned one).
	compatAppTooOld podCompat = "app_too_old"
	// This computer is not paired with the pod (firmware 4.0.1+, BP0081 over Ethernet or Wi-Fi):
	// pair it over USB or embeddedci.com (podpair.go).
	compatNotPaired podCompat = "not_paired"
)

// codeNotPaired is the pod's stable error code for an unpaired computer on the LAN (lan_not_paired).
const codeNotPaired = "BP0081"

// The firmware version in the MCP server's FirmwareTooOldError ("the pod runs firmware 3.10.13 (client API 1)").
// Only for the page to show; the kind comes from the error class.
var tooOldFirmware = regexp.MustCompile(`firmware (\d+\.[^\s()]+) \(client API`)

// The MCP server's error class, first in its tool error after the MCP library's own prefix
// ("Error executing tool connect: FirmwareTooOldError: ...").
var mcpErrorClass = regexp.MustCompile(`(?:^|: )(FirmwareTooOldError|ClientTooOldError|PodNotPairedError): `)

// A coded refusal as the MCP server words it: its class, the command, then the code
// ("PodLockedError: connect: BP0081: ..."), for a server whose SDK has no PodNotPairedError.
var mcpErrorCode = regexp.MustCompile(`(?:^|: )[A-Za-z]+Error: (?:[a-z_]+: )?(BP\d{4}): `)

// mcpCompat reads an MCP tool error: the MCP server's FirmwareTooOldError or ClientTooOldError,
// and the firmware it names, or its PodNotPairedError (BP0081).
func mcpCompat(text string) (podCompat, string) {
	text = strings.TrimSpace(text)
	m := mcpErrorClass.FindStringSubmatch(text)
	if m == nil {
		if c := mcpErrorCode.FindStringSubmatch(text); c != nil && c[1] == codeNotPaired {
			return compatNotPaired, ""
		}
		return compatOK, ""
	}
	switch m[1] {
	case "ClientTooOldError":
		return compatAppTooOld, ""
	case "PodNotPairedError":
		return compatNotPaired, ""
	}
	fw := ""
	if v := tooOldFirmware.FindStringSubmatch(text); v != nil {
		fw = v[1]
	}
	return compatFirmwareTooOld, fw
}

// The benchpod CLI's exit codes (benchpod-cli 0.10, README "Exit codes").
const (
	cliExitUsage       = 2 // a bad flag, argument or subcommand
	cliExitRefused     = 3 // the pod refused the command (BP0081: this computer is not paired)
	cliExitBusy        = 4 // the pod or one of its engines is in use (another session holds it)
	cliExitUnreachable = 5 // the pod did not answer
	cliExitUpdateCLI   = 6 // the pod's firmware needs a newer CLI: update the app, which bundles it
	cliExitUpdatePod   = 7 // the pod needs firmware 4.0: benchpod flash-self
)

// cliExitCode is the CLI's exit code, -1 when it did not run or was stopped.
func cliExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// The stable error code in the CLI's output: it words a coded refusal "BP0081: ...".
var cliErrorCode = regexp.MustCompile(`\b(BP\d{4}): `)

// cliCompat is what the CLI's exit code says about the pod's firmware. Exit 3 (refused) covers
// every refusal: it is the unpaired computer only with the code BP0081 in the CLI's output.
func cliCompat(code int, out string) podCompat {
	switch code {
	case cliExitRefused:
		for _, m := range cliErrorCode.FindAllStringSubmatch(out, -1) {
			if m[1] == codeNotPaired {
				return compatNotPaired
			}
		}
	case cliExitUpdatePod:
		return compatFirmwareTooOld
	case cliExitUpdateCLI:
		return compatAppTooOld
	}
	return compatOK
}

// compatHint is the line the app adds to a refusal of this kind: what to do.
func compatHint(c podCompat) string {
	switch c {
	case compatFirmwareTooOld:
		return "This app needs pod firmware 4.0 or later: update the pod in Settings, Pod firmware."
	case compatAppTooOld:
		return "The pod's firmware is newer than this app's benchpod CLI: update the BenchPod desktop app."
	case compatNotPaired:
		return "This computer is not paired with the pod: click pod not paired at the top to pair it over USB-C or embeddedci.com."
	}
	return ""
}
