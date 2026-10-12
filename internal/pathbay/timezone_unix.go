//go:build !windows

package pathbay

import (
	"os"
	"path/filepath"
	"strings"
)

func localTimezone() (string, []byte, error) {
	name := strings.TrimPrefix(os.Getenv("TZ"), ":")
	if name != "" {
		if name == "UTC" || name == "Etc/UTC" {
			return "UTC", nil, nil
		}
		if filepath.IsAbs(name) {
			data, err := os.ReadFile(name)
			return name, data, err
		}
		data, err := zoneBytes(name)
		return name, data, err
	}
	resolved, err := filepath.EvalSymlinks("/etc/localtime")
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", nil, err
	}
	name = "Local"
	if i := strings.Index(resolved, "/zoneinfo/"); i >= 0 {
		name = resolved[i+10:]
	}
	return name, data, nil
}
