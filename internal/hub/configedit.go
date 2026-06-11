// Package hub implements HuggingFace model browsing, downloading, and
// config.yaml management for the web UI.
package hub

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AddModelEntry appends a model entry under the top-level models: mapping,
// preserving comments and formatting elsewhere in the file. The cmd is
// written as a literal block scalar.
func AddModelEntry(configPath, modelID, name, cmd string) error {
	return editConfig(configPath, func(models *yaml.Node) error {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if name != "" {
			entry.Content = append(entry.Content,
				strNode("name", 0), strNode(name, 0))
		}
		entry.Content = append(entry.Content,
			strNode("cmd", 0), strNode(cmd+"\n", yaml.LiteralStyle))

		models.Content = append(models.Content,
			strNode(modelID, yaml.DoubleQuotedStyle), entry)
		return nil
	})
}

// RemoveModelEntry deletes a model entry by ID. Returns false if not found.
func RemoveModelEntry(configPath, modelID string) (bool, error) {
	found := false
	err := editConfig(configPath, func(models *yaml.Node) error {
		for i := 0; i+1 < len(models.Content); i += 2 {
			if models.Content[i].Value == modelID {
				models.Content = append(models.Content[:i], models.Content[i+2:]...)
				found = true
				return nil
			}
		}
		return errNoChange
	})
	if errors.Is(err, errNoChange) {
		return false, nil
	}
	return found, err
}

var errNoChange = errors.New("no change")

func strNode(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

// editConfig loads the raw YAML document, locates (or creates) the models:
// mapping, applies fn, and writes the document back atomically.
func editConfig(configPath string, fn func(models *yaml.Node) error) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a YAML mapping at the top level", configPath)
	}
	root := doc.Content[0]

	var models *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "models" {
			models = root.Content[i+1]
			break
		}
	}
	if models == nil {
		models = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, strNode("models", 0), models)
	}
	if models.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: models is not a mapping", configPath)
	}
	// Force block style so entries render multi-line with literal cmd
	// scalars even when the file had `models: {}` (flow style).
	models.Style = 0

	if err := fn(models); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encoding %s: %w", configPath, err)
	}
	enc.Close()

	return atomicWrite(configPath, buf.Bytes())
}

// atomicWrite replaces path with data via a temp file + rename in the same
// directory, preserving the original file mode.
func atomicWrite(path string, data []byte) error {
	mode := os.FileMode(0644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
