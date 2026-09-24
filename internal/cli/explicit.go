package cli

import "strings"

func explicitFlags(args []string) map[string]bool {
	result := map[string]bool{}
	for _, argument := range args {
		if argument == "--" {
			break
		}
		if !strings.HasPrefix(argument, "--") || argument == "--" {
			continue
		}
		name := strings.TrimPrefix(argument, "--")
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		if name != "" {
			result[name] = true
		}
	}
	return result
}

func setOverride(target map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := target
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func clearableList(values []string) []string {
	if len(values) == 1 && values[0] == "" {
		return []string{}
	}
	return append([]string(nil), values...)
}
