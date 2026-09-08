package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const codexConfig = `model_provider = "excursion-funnel"

[model_providers."excursion-funnel"]
name = "Excursion Funnel Proxy"
base_url = "http://127.0.0.1:8787/v1"
wire_api = "responses"
requires_openai_auth = true
`

// Codex adds the default ef provider to a Codex config file. Matching
// configurations, collisions, and invalid documents are never rewritten.
func Codex(configPath string) (changed bool, err error) {
	var (
		mode          = os.FileMode(0o600)
		data          []byte
		info, statErr = os.Lstat(configPath)
	)
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("Codex config at %s must be a regular file", configPath)
		}
		mode = info.Mode().Perm()
		data, err = os.ReadFile(configPath)
		if err != nil {
			return false, fmt.Errorf("read Codex config: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect Codex config: %w", statErr)
	}

	updated, changed, err := configureCodexTOML(data)
	if err != nil {
		return false, fmt.Errorf("Codex config in %s: %w; config was not modified", configPath, err)
	}
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return false, fmt.Errorf("create Codex config directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(configPath), ".ef-config-*")
	if err != nil {
		return false, fmt.Errorf("create temporary Codex config: %w", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(updated); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write Codex config: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("set Codex config permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close Codex config: %w", err)
	}
	if err := os.Rename(file.Name(), configPath); err != nil {
		return false, fmt.Errorf("replace Codex config: %w", err)
	}
	return true, nil
}

func configureCodexTOML(data []byte) ([]byte, bool, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return []byte(codexConfig), true, nil
	}
	settings := make(map[string]any)
	if err := toml.Unmarshal(data, &settings); err != nil {
		return nil, false, errors.New("expected valid TOML")
	}
	if provider, exists := settings["model_provider"]; exists && provider != "excursion-funnel" {
		return nil, false, errors.New("configuration collision: model_provider is already set")
	}
	providers, exists := settings["model_providers"]
	if exists {
		providerMap, ok := providers.(map[string]any)
		if !ok {
			return nil, false, errors.New("model_providers must be a TOML table")
		}
		if provider, exists := providerMap["excursion-funnel"]; exists {
			if !matchesCodexProvider(provider) {
				return nil, false, errors.New("configuration collision: model_providers.excursion-funnel is already set")
			}
			if _, exists := settings["model_provider"]; exists {
				return nil, false, nil
			}
			return nil, false, errors.New("configuration collision: model_providers.excursion-funnel requires model_provider")
		}
	}

	var output strings.Builder
	output.Grow(len(data) + len(codexConfig) + 1)
	if _, exists := settings["model_provider"]; !exists {
		output.WriteString(`model_provider = "excursion-funnel"`)
		output.WriteByte('\n')
	}
	output.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		output.WriteByte('\n')
	}
	output.WriteByte('\n')
	output.WriteString(`[model_providers."excursion-funnel"]`)
	output.WriteByte('\n')
	output.WriteString(`name = "Excursion Funnel Proxy"`)
	output.WriteByte('\n')
	output.WriteString(`base_url = "http://127.0.0.1:8787/v1"`)
	output.WriteByte('\n')
	output.WriteString(`wire_api = "responses"`)
	output.WriteByte('\n')
	output.WriteString(`requires_openai_auth = true`)
	output.WriteByte('\n')
	return []byte(output.String()), true, nil
}

func matchesCodexProvider(value any) bool {
	provider, ok := value.(map[string]any)
	if !ok || len(provider) != 4 {
		return false
	}
	return provider["name"] == "Excursion Funnel Proxy" &&
		provider["base_url"] == "http://127.0.0.1:8787/v1" &&
		provider["wire_api"] == "responses" &&
		provider["requires_openai_auth"] == true
}
