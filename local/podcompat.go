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
// `benchpod flash-self`, so the app offers that update.

// podCompat is why the app cannot drive the pod it reached, "" when it can.
type podCompat string

const (
	compatOK podCompat = ""
	// The pod runs firmware 3.x: update the pod (Settings, Pod firmware).
	compatFirmwareTooOld podCompat = "firmware_too_old"
	// The pod's firmware is newer than the MCP server or the CLI the app runs: update the app (or
	// the embeddedci-mcp on PATH, which the app runs instead of its pinned one).
	compatAppTooOld podCompat = "app_too_old"
)

// The firmware version in the MCP server's FirmwareTooOldError ("the pod runs firmware 3.10.13 (client API 1)").
// Only for the page to show; the kind comes from the error class.
var tooOldFirmware = regexp.MustCompile(`firmware (\d+\.[^\s()]+) \(client API`)

// The MCP server's error class, first in its tool error after the MCP library's own prefix
// ("Error executing tool connect: FirmwareTooOldError: ...").
var mcpErrorClass = regexp.MustCompile(`(?:^|: )(FirmwareTooOldError|ClientTooOldError): `)

// mcpCompat reads an MCP tool error: the MCP server's FirmwareTooOldError or ClientTooOldError,
// and the firmware it names.
func mcpCompat(text string) (podCompat, string) {
	m := mcpErrorClass.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return compatOK, ""
	}
	if m[1] == "ClientTooOldError" {
		return compatAppTooOld, ""
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
	cliExitRefused     = 3 // the pod refused the command
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

// cliCompat is what the CLI's exit code says about the pod's firmware.
func cliCompat(code int) podCompat {
	switch code {
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
	}
	return ""
}
