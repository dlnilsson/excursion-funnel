// Package setup configures clients for the local ef proxy.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const claudeBaseURL = "http://127.0.0.1:8787"

// Claude sets the default proxy URL when the setting is missing or empty.
// Matching settings, collisions, and invalid documents are never rewritten.
func Claude(settingsPath string) (changed bool, err error) {
	var (
		mode          = os.FileMode(0o600)
		data          = []byte("{}")
		info, statErr = os.Lstat(settingsPath)
	)
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("Claude settings at %s must be a regular file", settingsPath)
		}
		mode = info.Mode().Perm()
		data, err = os.ReadFile(settingsPath)
		if err != nil {
			return false, fmt.Errorf("read Claude settings: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect Claude settings: %w", statErr)
	}

	updated, changed, err := configureClaudeJSON(data)
	if err != nil {
		return false, fmt.Errorf("Claude settings in %s: %w; settings were not modified", settingsPath, err)
	}
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return false, fmt.Errorf("create Claude settings directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(settingsPath), ".ef-settings-*")
	if err != nil {
		return false, fmt.Errorf("create temporary Claude settings: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(updated); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write Claude settings: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("set Claude settings permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close Claude settings: %w", err)
	}
	if err := os.Rename(file.Name(), settingsPath); err != nil {
		return false, fmt.Errorf("replace Claude settings: %w", err)
	}
	return true, nil
}

func configureClaudeJSON(data []byte) ([]byte, bool, error) {
	var (
		settings map[string]json.RawMessage
		env      = make(map[string]json.RawMessage)
	)
	if err := json.Unmarshal(data, &settings); err != nil || settings == nil {
		return nil, false, errors.New("expected a valid JSON object")
	}
	if raw, exists := settings["env"]; exists {
		if err := json.Unmarshal(raw, &env); err != nil || env == nil {
			return nil, false, errors.New("env must be a JSON object")
		}
	}
	if raw, exists := env["ANTHROPIC_BASE_URL"]; exists {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return nil, false, errors.New("env.ANTHROPIC_BASE_URL must be a string")
		}
		switch *value {
		case claudeBaseURL:
			return nil, false, nil
		case "":
		default:
			return nil, false, errors.New("configuration collision: env.ANTHROPIC_BASE_URL is already set")
		}
	}
	env["ANTHROPIC_BASE_URL"] = json.RawMessage(`"` + claudeBaseURL + `"`)
	encodedEnv, err := json.Marshal(env)
	if err != nil {
		return nil, false, fmt.Errorf("encode Claude environment: %w", err)
	}
	settings["env"] = encodedEnv
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("encode Claude settings: %w", err)
	}
	return append(updated, '\n'), true, nil
}
