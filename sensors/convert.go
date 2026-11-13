package sensors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"gopkg.in/yaml.v3"
)

// Key order is significant in a definition (format.md): registers fill in order, state updates
// run in order. These conversions keep it, which a Go map would not.

// orderedJSON returns the definition as compact JSON with its keys in the file's order.
func orderedJSON(src []byte, format string) ([]byte, error) {
	if format == SourceJSON {
		var buf bytes.Buffer
		if err := json.Compact(&buf, bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, &root, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSON(buf *bytes.Buffer, n *yaml.Node, depth int) error {
	if depth > 200 {
		return errors.New("nested too deeply")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return errors.New("not one document")
		}
		return writeJSON(buf, n.Content[0], depth+1)
	case yaml.AliasNode:
		return writeJSON(buf, n.Alias, depth+1)
	case yaml.MappingNode:
		buf.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				buf.WriteByte(',')
			}
			k, _ := json.Marshal(n.Content[i].Value)
			buf.Write(k)
			buf.WriteByte(':')
			if err := writeJSON(buf, n.Content[i+1], depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSON(buf, c, depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return err
		}
		if f, ok := v.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return fmt.Errorf("line %d: %s has no JSON form", n.Line, n.Value)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(b)
	default:
		return fmt.Errorf("line %d: unexpected YAML node", n.Line)
	}
	return nil
}

// ToJSON returns the definition as indented JSON, keys in order (a JSON source as written).
func ToJSON(def *Definition) []byte {
	if def.SourceFormat == SourceJSON {
		return def.Source
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, def.JSON, "", "  "); err != nil {
		return def.JSON
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// ToYAML returns the definition as YAML, keys in order (a YAML source as written).
func ToYAML(def *Definition) ([]byte, error) {
	if def.SourceFormat == SourceYAML {
		return def.Source, nil
	}
	var root yaml.Node
	// JSON is YAML (flow style): read it as nodes, then write them in block style.
	if err := yaml.Unmarshal(def.JSON, &root); err != nil {
		return nil, err
	}
	blockStyle(&root)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

// blockStyle drops the flow and quoting styles JSON gave the nodes; the encoder quotes a string
// that would read back as another type.
func blockStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		blockStyle(c)
	}
}

// renameJSON sets the top-level name of a JSON source, keys in order, indented.
func renameJSON(def *Definition, newName string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(def.JSON, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the top level must be an object")
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "name" {
			m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: newName}
		}
	}
	var compact bytes.Buffer
	if err := writeJSON(&compact, &root, 0); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// renameYAMLNodes sets the top-level name of a YAML source by re-encoding it (keys in order;
// comments the encoder can attach stay).
func renameYAMLNodes(def *Definition, newName string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(def.Source, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the top level must be a mapping")
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "name" {
			m.Content[i+1].Value, m.Content[i+1].Tag, m.Content[i+1].Style = newName, "!!str", 0
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}
