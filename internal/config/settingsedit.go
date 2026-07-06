package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"gopkg.in/yaml.v3"
)

var errNoSettingsChange = errors.New("no change")

// AddAPIKey generates a new random API key and a separate non-secret id,
// appends {id, key, label, createdAt} under apiKeys:, and returns the id and
// the plaintext key. The key is not stored anywhere else in plaintext after
// this call returns; callers must surface it to the operator immediately.
func AddAPIKey(configPath, label string) (id string, key string, err error) {
	keyBytes := make([]byte, 32)
	if _, err = rand.Read(keyBytes); err != nil {
		return "", "", err
	}
	key = "sk-" + base64.RawURLEncoding.EncodeToString(keyBytes)

	idBytes := make([]byte, 6)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", err
	}
	id = hex.EncodeToString(idBytes)

	createdAt := time.Now().UTC().Format(time.RFC3339)

	err = EditConfig(configPath, func(root *yaml.Node) error {
		keys, err := SequenceChild(root, "apiKeys")
		if err != nil {
			return err
		}
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.Content = append(entry.Content,
			StrNode("id", 0), StrNode(id, 0),
			StrNode("key", 0), StrNode(key, yaml.DoubleQuotedStyle),
		)
		if label != "" {
			entry.Content = append(entry.Content, StrNode("label", 0), StrNode(label, yaml.DoubleQuotedStyle))
		}
		entry.Content = append(entry.Content, StrNode("createdAt", 0), StrNode(createdAt, 0))
		keys.Content = append(keys.Content, entry)
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return id, key, nil
}

// RemoveAPIKey deletes the apiKeys: entry with the given id. Returns false
// if no entry has that id; this includes legacy bare-string entries, which
// have no id and so can only be removed by hand-editing the file.
func RemoveAPIKey(configPath, id string) (bool, error) {
	found := false
	err := EditConfig(configPath, func(root *yaml.Node) error {
		keys, err := SequenceChild(root, "apiKeys")
		if err != nil {
			return err
		}
		for i, item := range keys.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(item.Content); j += 2 {
				if item.Content[j].Value == "id" && item.Content[j+1].Value == id {
					keys.Content = append(keys.Content[:i], keys.Content[i+1:]...)
					found = true
					return nil
				}
			}
		}
		return errNoSettingsChange
	})
	if errors.Is(err, errNoSettingsChange) {
		return false, nil
	}
	return found, err
}

// SetAuthCredentials writes (or, if both are empty, clears) the top-level
// auth: mapping.
func SetAuthCredentials(configPath, username, password string) error {
	return EditConfig(configPath, func(root *yaml.Node) error {
		auth, err := MappingChild(root, "auth")
		if err != nil {
			return err
		}
		auth.Content = nil
		if username != "" || password != "" {
			auth.Content = append(auth.Content,
				StrNode("username", 0), StrNode(username, yaml.DoubleQuotedStyle),
				StrNode("password", 0), StrNode(password, yaml.DoubleQuotedStyle),
			)
		}
		return nil
	})
}
