package sensors

import (
	"errors"
	"strings"
	"testing"
)

const bmeYAML = `format: 1
kind: i2c
name: bme280   # library name
label: "Bosch BME280: temperature, pressure and humidity"
description: |
  name: not the top level, an indented block
params:
  - { key: temperature_c, unit: C, min: -100, max: 150, default: 25 }
i2c:
  address: 0x76
`

func TestParseYAML(t *testing.T) {
	def, err := Parse([]byte(bmeYAML))
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != KindI2C || def.Name != "bme280" || def.SourceFormat != SourceYAML || def.Format != 1 {
		t.Fatalf("got %+v", def)
	}
	if !strings.Contains(string(def.JSON), `"address":118`) {
		t.Fatalf("hex int not parsed: %s", def.JSON)
	}
}

func TestParseJSON(t *testing.T) {
	def, err := Parse([]byte(`  {"format":1,"kind":"stimulus","name":"hot_afternoon","signals":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != KindStimulus || def.SourceFormat != SourceJSON || def.Label != "" {
		t.Fatalf("got %+v", def)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"empty":             {"   \n", "empty"},
		"not a mapping":     {"- a\n- b\n", "mapping"},
		"bad yaml":          {"format: 1\nkind: [\n", "invalid YAML"},
		"bad json":          {`{"format":1,`, "invalid JSON"},
		"json trailing":     {`{"format":1} {}`, "after the top-level"},
		"two docs":          {"format: 1\n---\nformat: 1\n", "more than one"},
		"no format":         {"kind: i2c\nname: x\nlabel: x\n", "format: missing"},
		"format 2":          {"format: 2\nkind: i2c\nname: x\nlabel: x\n", "format: is 2"},
		"unknown kind":      {"format: 1\nkind: can\nname: x\nlabel: x\n", "unknown kind"},
		"bad name":          {"format: 1\nkind: i2c\nname: BME-280\nlabel: x\n", "name:"},
		"long name":         {"format: 1\nkind: i2c\nname: " + strings.Repeat("a", 33) + "\nlabel: x\n", "name:"},
		"no label":          {"format: 1\nkind: i2c\nname: x\n", "label: missing"},
		"unknown key":       {"format: 1\nkind: i2c\nname: x\nlabel: x\nregister: []\n", "register: unknown top-level key"},
		"key of other kind": {"format: 1\nkind: analog\nname: x\nlabel: x\ni2c: {}\n", "i2c: unknown"},
		"too big":           {"format: 1\n#" + strings.Repeat("x", MaxSourceBytes) + "\n", "at most"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
		var se *Error
		if err != nil && !errors.As(err, &se) {
			t.Errorf("%s: not a *Error: %T", name, err)
		}
	}
}

func TestRenameYAMLKeepsComments(t *testing.T) {
	def, err := Parse([]byte(bmeYAML))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Rename(def, "bme280_copy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "name: bme280_copy   # library name\n") {
		t.Fatalf("rename lost the comment:\n%s", out)
	}
	if !strings.Contains(string(out), "  name: not the top level") {
		t.Fatalf("rename touched the block scalar:\n%s", out)
	}
	re, err := Parse(out)
	if err != nil || re.Name != "bme280_copy" {
		t.Fatalf("re-parse: %v %+v", err, re)
	}
}

func TestRenameJSON(t *testing.T) {
	def, err := Parse([]byte(`{"format":1,"kind":"stimulus","name":"a","signals":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Rename(def, "b")
	if err != nil {
		t.Fatal(err)
	}
	re, err := Parse(out)
	if err != nil || re.Name != "b" || re.SourceFormat != SourceJSON {
		t.Fatalf("re-parse: %v %+v", err, re)
	}
}

const orderYAML = `format: 1
kind: i2c
name: order
label: Order
state:
  - { name: zeta, init: "1" }
  - { name: alpha, init: "zeta + 1" }
registers:
  - { at: 0x10, u8: "zeta" }
i2c:
  address: 0x40
`

func TestKeyOrderKept(t *testing.T) {
	def, err := Parse([]byte(orderYAML))
	if err != nil {
		t.Fatal(err)
	}
	js := string(def.JSON)
	if !strings.HasPrefix(js, `{"format":1,"kind":"i2c","name":"order","label":"Order","state":[{"name":"zeta"`) ||
		!strings.Contains(js, `"registers":[{"at":16,"u8":"zeta"}]`) {
		t.Fatalf("json = %s", js)
	}
	// JSON source -> YAML and back keeps the order.
	jdef, err := Parse(ToJSON(def))
	if err != nil {
		t.Fatal(err)
	}
	y, err := ToYAML(jdef)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(y)
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	if string(back.JSON) != js {
		t.Fatalf("round trip:\n%s\n%s", back.JSON, js)
	}
	renamed, err := Rename(jdef, "order2")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(renamed)
	if err != nil || r.Name != "order2" || !strings.HasPrefix(string(r.JSON), `{"format":1,"kind":"i2c","name":"order2","label"`) {
		t.Fatalf("rename json: %v %s", err, renamed)
	}
}

func TestToYAMLQuotesStringsThatLookLikeNumbers(t *testing.T) {
	def, err := Parse([]byte(`{"format":1,"kind":"analog","name":"a","label":"A","output":"voltage","value":"1.65"}`))
	if err != nil {
		t.Fatal(err)
	}
	y, _ := ToYAML(def)
	back, err := Parse(y)
	if err != nil || string(back.JSON) != string(def.JSON) {
		t.Fatalf("%v\n%s", err, y)
	}
}

// Word registers (i2c.register_bytes: 2: tmp117, ina219) and the 16-bit command map (sht3x,
// scd4x) pass the header check; every embedded entry parses.
func TestEmbeddedEntriesParse(t *testing.T) {
	lib, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Entries) != 17 {
		t.Fatalf("embedded copy has %d entries, want 17", len(lib.Entries))
	}
	for _, e := range lib.Entries {
		_, src, ok := lib.Entry(e.Name)
		if !ok {
			t.Fatalf("%s: no source", e.Name)
		}
		def, err := Parse(src)
		if err != nil {
			t.Errorf("%s: %v", e.Name, err)
			continue
		}
		if def.Name != e.Name || def.Kind != e.Kind {
			t.Errorf("%s: parsed as %s/%s", e.Name, def.Name, def.Kind)
		}
	}
	for _, name := range []string{"tmp117", "ina219", "sht3x", "scd4x"} {
		if _, _, ok := lib.Entry(name); !ok {
			t.Errorf("%s missing from the embedded copy", name)
		}
	}
	def, err := Parse([]byte("format: 1\nkind: i2c\nname: w\nlabel: Word sensor\ni2c: { address: 0x48, register_bytes: 2 }\nregisters:\n  - { at: 0x00, u16be: 1 }\n"))
	if err != nil || !strings.Contains(string(def.JSON), `"register_bytes":2`) {
		t.Fatalf("register_bytes: %v %s", err, def.JSON)
	}
}
