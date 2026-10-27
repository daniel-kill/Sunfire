package main

import (
	"os"
	"path/filepath"
)

// applicationDir is the deployment root, overridable for packaged installs.
func applicationDir() string {
	if dir := os.Getenv("SUNFIRE_HOME"); dir != "" {
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// dataDir preserves the local Data/ default while allowing persistent state to
// live outside the release directory in service deployments.
func dataDir() string {
	if dir := os.Getenv("SUNFIRE_DATA_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(applicationDir(), "Data")
}

func dataPath(parts ...string) string {
	return filepath.Join(append([]string{dataDir()}, parts...)...)
}

func applicationPath(parts ...string) string {
	return filepath.Join(append([]string{applicationDir()}, parts...)...)
}
