package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// maxEnvironment is the agent's limit on a stack's environment entries.
const maxEnvironment = 128

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnvFile reads a stack environment: a JSON object of strings, or
// KEY=VALUE lines with blank lines and # comments skipped. A value is
// everything after the first "=", taken as is: no quotes, no interpolation.
// Errors name lines and keys, never values.
func parseEnvFile(data []byte) (map[string]string, error) {
	env := map[string]string{}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '{' {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("environment file is not a JSON object of strings")
		}
		for key, value := range raw {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("environment %s: the value must be a string", key)
			}
			if !envKey.MatchString(key) {
				return nil, fmt.Errorf("environment key %q is not a valid name", key)
			}
			env[key] = s
		}
	} else {
		for i, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("environment file line %d: expected KEY=VALUE", i+1)
			}
			if !envKey.MatchString(key) {
				return nil, fmt.Errorf("environment file line %d: %q is not a valid name", i+1, key)
			}
			if _, seen := env[key]; seen {
				return nil, fmt.Errorf("environment file line %d: %s is set twice", i+1, key)
			}
			env[key] = value
		}
	}
	if len(env) > maxEnvironment {
		return nil, fmt.Errorf("environment has %d entries; a stack takes at most %d", len(env), maxEnvironment)
	}
	return env, nil
}
