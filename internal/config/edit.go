package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// editMu serializes EditConfig's read-modify-write cycle across the whole
// process. Without it, concurrent callers (e.g. a hub download completing at
// the same time a Settings API key is generated) race on the same config
// file: each reads the pre-edit version, applies its own change in memory,
// then writes back — last writer wins and silently discards every other
// concurrent edit, including unrelated pre-existing entries the racing
// writer's stale read didn't have. See commit 9fe5619 for the original
// regression this guards against.
var editMu sync.Mutex

// EditConfig loads the raw YAML document at configPath, hands the top-level
// mapping node to fn for in-place mutation, and writes the document back
// atomically. Comments and formatting elsewhere in the file are preserved.
func EditConfig(configPath string, fn func(root *yaml.Node) error) error {
	editMu.Lock()
	defer editMu.Unlock()

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

	if err := fn(root); err != nil {
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

// MappingChild finds root's top-level child mapping named key, creating and
// appending an empty one to root if absent. It forces block style so new
// entries render multi-line even when the section previously had `key: {}`
// flow-style content.
func MappingChild(root *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			node := root.Content[i+1]
			if node.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s is not a mapping", key)
			}
			node.Style = 0
			return node, nil
		}
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, StrNode(key, 0), node)
	return node, nil
}

// SequenceChild finds root's top-level child sequence named key, creating
// and appending an empty one to root if absent.
func SequenceChild(root *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			node := root.Content[i+1]
			if node.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%s is not a sequence", key)
			}
			node.Style = 0
			return node, nil
		}
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, StrNode(key, 0), node)
	return node, nil
}

// StrNode builds a scalar string YAML node with the given style (0 for plain).
func StrNode(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

// atomicWrite replaces path with data via a temp file + rename in the same
// directory. The file is always written with owner-only (0600) permissions,
// regardless of whatever mode the file previously had: config.yaml can now
// hold a plaintext auth password and plaintext API keys, so it must never be
// left group- or world-readable.
func atomicWrite(path string, data []byte) error {
	const mode = os.FileMode(0600)
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
