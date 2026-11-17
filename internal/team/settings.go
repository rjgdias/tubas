package team

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Settings are the team.yaml values the settings screen changes.
type Settings struct {
	Routing string // lead, router or all
	Lead    string
	MaxHops int
	Notify  string // all, window or off
}

// SaveSettings changes those values in team.yaml and keeps everything
// else as it was, comments included: it edits the YAML tree instead of
// rewriting the file from a struct.
func SaveSettings(dir string, s Settings) error {
	switch s.Routing {
	case "lead", "router", "all":
	default:
		return errors.New("routing must be lead, router or all")
	}
	if s.MaxHops < 1 || s.MaxHops > 50 {
		return errors.New("hops must be between 1 and 50")
	}
	switch s.Notify {
	case "", "all", "window", "off":
	default:
		return errors.New("notify must be all, window or off")
	}
	path := filepath.Join(dir, "team.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("team.yaml: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("team.yaml: expected a mapping at the top")
	}
	root := doc.Content[0]
	setScalar(root, "routing", s.Routing, "!!str")
	setScalar(root, "lead", s.Lead, "!!str")
	setScalar(root, "max_hops", strconv.Itoa(s.MaxHops), "!!int")
	if s.Notify != "" {
		setScalar(root, "notify", s.Notify, "!!str")
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	enc.Close()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// setScalar sets key: value in a mapping, adding the key when missing.
func setScalar(m *yaml.Node, key, value, tag string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Kind, m.Content[i+1].Value, m.Content[i+1].Tag = yaml.ScalarNode, value, tag
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag})
}
