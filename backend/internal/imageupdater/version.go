package imageupdater

import (
	"fmt"
	"strings"
)

func NormalizeVersion(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("invalid version %q", value)
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid version %q", value)
	}
	for _, part := range parts {
		if part == "" {
			return "", fmt.Errorf("invalid version %q", value)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("invalid version %q", value)
			}
		}
	}
	return value, nil
}

func ImageReference(repository, version string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" || strings.ContainsAny(repository, " \t\r\n") {
		return "", fmt.Errorf("invalid image repository")
	}
	normalized, err := NormalizeVersion(version)
	if err != nil {
		return "", err
	}
	return repository + ":" + normalized, nil
}
