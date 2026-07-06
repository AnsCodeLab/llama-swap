// Package hub implements HuggingFace model browsing, downloading, and
// config.yaml management for the web UI.
package hub

import (
	"errors"

	"github.com/mostlygeek/llama-swap/internal/config"
	"gopkg.in/yaml.v3"
)

// AddModelEntry appends a model entry under the top-level models: mapping,
// preserving comments and formatting elsewhere in the file. The cmd is
// written as a literal block scalar.
func AddModelEntry(configPath, modelID, name, cmd string) error {
	return config.EditConfig(configPath, func(root *yaml.Node) error {
		models, err := config.MappingChild(root, "models")
		if err != nil {
			return err
		}

		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if name != "" {
			entry.Content = append(entry.Content,
				config.StrNode("name", 0), config.StrNode(name, 0))
		}
		entry.Content = append(entry.Content,
			config.StrNode("cmd", 0), config.StrNode(cmd+"\n", yaml.LiteralStyle))

		models.Content = append(models.Content,
			config.StrNode(modelID, yaml.DoubleQuotedStyle), entry)
		return nil
	})
}

// RemoveModelEntry deletes a model entry by ID. Returns false if not found.
func RemoveModelEntry(configPath, modelID string) (bool, error) {
	found := false
	err := config.EditConfig(configPath, func(root *yaml.Node) error {
		models, err := config.MappingChild(root, "models")
		if err != nil {
			return err
		}
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
