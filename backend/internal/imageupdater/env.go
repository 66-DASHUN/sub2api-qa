package imageupdater

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const versionEnvKey = "SUB2API_VERSION="

func ReadEnvVersion(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	version := ""
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, versionEnvKey) {
			continue
		}
		count++
		version = strings.TrimPrefix(line, versionEnvKey)
	}
	if count == 0 {
		return "", fmt.Errorf("%s is missing", strings.TrimSuffix(versionEnvKey, "="))
	}
	if count > 1 {
		return "", fmt.Errorf("duplicate %s entries", strings.TrimSuffix(versionEnvKey, "="))
	}
	return NormalizeVersion(version)
}

func UpdateEnvVersion(path, version string) error {
	normalized, err := NormalizeVersion(version)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlinked env file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	newline := "\n"
	if strings.Contains(string(body), "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(string(body), "\n")
	count := 0
	for i, line := range lines {
		suffix := ""
		plain := line
		if strings.HasSuffix(plain, "\r") {
			plain = strings.TrimSuffix(plain, "\r")
			suffix = "\r"
		}
		if !strings.HasPrefix(plain, versionEnvKey) {
			continue
		}
		count++
		lines[i] = versionEnvKey + normalized + suffix
	}
	if count > 1 {
		return fmt.Errorf("duplicate %s entries", strings.TrimSuffix(versionEnvKey, "="))
	}
	updated := strings.Join(lines, "\n")
	if count == 0 {
		updated = strings.TrimSuffix(updated, "\n")
		updated = strings.TrimSuffix(updated, "\r")
		if updated != "" {
			updated += newline
		}
		updated += versionEnvKey + normalized + newline
	}
	return atomicWriteFile(path, []byte(updated), info.Mode().Perm())
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".sub2api-image-updater-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}
