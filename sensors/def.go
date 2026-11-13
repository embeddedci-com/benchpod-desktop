// Package sensors keeps BenchPod sensor definitions and stimulus files (benchpod-sensors
// docs/format.md, format version 1) on this computer, and serves the public sensor library.
//
// A definition is a YAML file (JSON with the same structure is accepted). Parse checks what the
// app needs to store and list one: it parses, the header is right (format, kind, name, label),
// the size is within the limit, and no unknown top-level key is present. Everything below the top
// level (expressions, registers, messages) is checked in the page by the pod's own engine
// compiled to WebAssembly, which also previews the definition. The same rules as the EmbeddedCI
// web app's server.
package sensors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FormatVersion is the definition format this package understands.
const FormatVersion = 1

// MaxSourceBytes bounds a definition, as source text and as JSON (format.md, section 1).
const MaxSourceBytes = 32 * 1024

// MaxLabelLen bounds the label, which the UI shows in lists.
const MaxLabelLen = 200

// Source formats.
const (
	SourceYAML = "yaml"
	SourceJSON = "json"
)

// Kinds a definition may have. "stimulus" is a stimulus file, the others are sensors.
const (
	KindI2C      = "i2c"
	KindSPI      = "spi"
	KindUART     = "uart"
	KindAnalog   = "analog"
	KindStimulus = "stimulus"
)

// Kinds lists every known kind, in display order.
var Kinds = []string{KindI2C, KindSPI, KindUART, KindAnalog, KindStimulus}

// NameRe is the pattern a name must match (format.md, section 1).
var NameRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// The top-level keys each kind allows (format.md, sections 1 and 3 to 7). Unknown keys are
// errors, so a typo never goes unnoticed.
var (
	sensorHeaderKeys = []string{"format", "kind", "name", "label", "description", "datasheet",
		"version", "license", "params", "constants", "functions", "let", "state"}
	registerKeys = []string{"registers", "commands", "conversion", "on_write", "data_ready",
		"fifo", "fill"}
	topLevelKeys = map[string][]string{
		KindI2C:    concat(sensorHeaderKeys, registerKeys, []string{"i2c"}),
		KindSPI:    concat(sensorHeaderKeys, registerKeys, []string{"spi"}),
		KindUART:   concat(sensorHeaderKeys, []string{"uart", "messages", "requests", "fragments"}),
		KindAnalog: concat(sensorHeaderKeys, []string{"output", "value", "range", "excitation"}),
		KindStimulus: {"format", "kind", "name", "label", "description", "version", "license",
			"repeat", "rate_hz", "signals", "faults"},
	}
)

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// AllowedTopLevelKeys returns the top-level keys a kind allows (nil for an unknown kind).
func AllowedTopLevelKeys(kind string) []string {
	keys := append([]string(nil), topLevelKeys[kind]...)
	sort.Strings(keys)
	return keys
}

// KnownKind reports whether kind is one of Kinds.
func KnownKind(kind string) bool {
	_, ok := topLevelKeys[kind]
	return ok
}

// Error is a validation error at a place in the file. Path is the location in the format.md
// error style (`name`, `registers[3].s16be`); empty for the file as a whole.
type Error struct {
	Path string `json:"path,omitempty"`
	Msg  string `json:"message"`
}

func (e *Error) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

func errAt(path, format string, args ...any) *Error {
	return &Error{Path: path, Msg: fmt.Sprintf(format, args...)}
}

// Definition is a parsed definition.
type Definition struct {
	Format int
	Kind   string
	Name   string
	Label  string
	// SourceFormat is how the source was written: SourceYAML or SourceJSON.
	SourceFormat string
	// Source is the text as given, kept verbatim so comments survive a download.
	Source []byte
	// JSON is the parsed definition as JSON, the form the pod receives.
	JSON []byte
	// Doc is the parsed definition.
	Doc map[string]any
}

// DetectFormat says whether src is JSON (its first non-space byte is '{') or YAML.
func DetectFormat(src []byte) string {
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf")), " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return SourceJSON
	}
	return SourceYAML
}

// Parse parses src (YAML or JSON) and checks the header. It does not run the full validator.
func Parse(src []byte) (*Definition, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, errAt("", "the file is empty")
	}
	if len(src) > MaxSourceBytes {
		return nil, errAt("", "the file is %d bytes; a definition may be at most %d bytes", len(src), MaxSourceBytes)
	}
	format := DetectFormat(src)
	var doc map[string]any
	var err error
	if format == SourceJSON {
		doc, err = parseJSON(src)
	} else {
		doc, err = parseYAML(src)
	}
	if err != nil {
		return nil, err
	}
	js, err := orderedJSON(src, format)
	if err != nil {
		return nil, errAt("", "cannot convert to JSON: %v", err)
	}
	if len(js) > MaxSourceBytes {
		return nil, errAt("", "the definition is %d bytes as JSON; at most %d bytes are allowed", len(js), MaxSourceBytes)
	}
	def := &Definition{SourceFormat: format, Source: src, JSON: js, Doc: doc}
	if err := checkHeader(def); err != nil {
		return nil, err
	}
	return def, nil
}

func parseJSON(src []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errAt("", "invalid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errAt("", "invalid JSON: unexpected data after the top-level object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errAt("", "the top level must be an object")
	}
	return m, nil
}

func parseYAML(src []byte) (map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errAt("", "the file is empty")
		}
		return nil, errAt("", "invalid YAML: %v", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errAt("", "the file holds more than one YAML document; put each definition in its own file")
	} else if !errors.Is(err, io.EOF) {
		return nil, errAt("", "invalid YAML: %v", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errAt("", "the top level must be a mapping (key: value)")
	}
	var v any
	if err := root.Content[0].Decode(&v); err != nil {
		return nil, errAt("", "invalid YAML: %v", err)
	}
	m, ok := normalize(v).(map[string]any)
	if !ok {
		return nil, errAt("", "the top level must be a mapping (key: value)")
	}
	return m, nil
}

// normalize turns the YAML decoder's maps into map[string]any, so the result marshals to JSON.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = normalize(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	default:
		return v
	}
}

func checkHeader(def *Definition) error {
	doc := def.Doc
	// format
	fv, ok := doc["format"]
	if !ok {
		return errAt("format", "missing; this app reads format %d", FormatVersion)
	}
	n, ok := asInt(fv)
	if !ok || n != FormatVersion {
		return errAt("format", "is %v; this app reads format %d", fv, FormatVersion)
	}
	def.Format = n
	// kind
	kind, ok := doc["kind"].(string)
	if !ok {
		if _, present := doc["kind"]; !present {
			return errAt("kind", "missing; one of %s", strings.Join(Kinds, ", "))
		}
		return errAt("kind", "must be one of %s", strings.Join(Kinds, ", "))
	}
	if !KnownKind(kind) {
		return errAt("kind", "unknown kind %q; one of %s", kind, strings.Join(Kinds, ", "))
	}
	def.Kind = kind
	// name
	name, ok := doc["name"].(string)
	if !ok {
		if _, present := doc["name"]; !present {
			return errAt("name", "missing")
		}
		return errAt("name", "must be a string of a-z, 0-9 and _ (at most 32)")
	}
	if !NameRe.MatchString(name) {
		return errAt("name", "%q must be 1 to 32 characters of a-z, 0-9 and _", name)
	}
	def.Name = name
	// label: required for a sensor, optional for a stimulus.
	if lv, present := doc["label"]; present {
		label, ok := lv.(string)
		if !ok {
			return errAt("label", "must be a string")
		}
		if len(label) > MaxLabelLen {
			return errAt("label", "is %d characters; at most %d", len(label), MaxLabelLen)
		}
		def.Label = strings.TrimSpace(label)
	}
	if def.Label == "" && kind != KindStimulus {
		return errAt("label", "missing; a sensor needs a label")
	}
	// unknown top-level keys
	allowed := map[string]bool{}
	for _, k := range topLevelKeys[kind] {
		allowed[k] = true
	}
	var unknown []string
	for k := range doc {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return errAt(unknown[0], "unknown top-level key for kind %s (allowed: %s)", kind,
			strings.Join(AllowedTopLevelKeys(kind), ", "))
	}
	return nil
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		if t == float64(int(t)) {
			return int(t), true
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// yamlNameLine is a top-level `name:` line: the key at column 0, its value, an optional comment.
var yamlNameLine = regexp.MustCompile(`(?m)^name:[ \t]*("[^"\n]*"|'[^'\n]*'|[^#\n]*?)([ \t]+#[^\n]*)?[ \t]*$`)

// Rename returns def's source with its name changed to newName. A YAML source keeps its text and
// comments (only the top-level `name:` line changes); a JSON source is re-encoded, indented.
func Rename(def *Definition, newName string) ([]byte, error) {
	if !NameRe.MatchString(newName) {
		return nil, errAt("name", "%q must be 1 to 32 characters of a-z, 0-9 and _", newName)
	}
	if def.SourceFormat == SourceYAML {
		locs := yamlNameLine.FindAllSubmatchIndex(def.Source, -1)
		if len(locs) == 1 {
			loc := locs[0]
			var out bytes.Buffer
			out.Write(def.Source[:loc[2]])
			out.WriteString(newName)
			out.Write(def.Source[loc[3]:])
			if re, err := Parse(out.Bytes()); err == nil && re.Name == newName {
				return out.Bytes(), nil
			}
		}
		// Unusual layout (flow style, a multi-line name): re-encode instead.
		return renameYAMLNodes(def, newName)
	}
	return renameJSON(def, newName)
}
