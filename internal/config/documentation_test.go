package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadmeRestrictsEnvironmentFileBeforeCreation(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	deployment := string(readme)
	start := strings.Index(deployment, `API_KEY="$(openssl rand -hex 32)"`)
	if start < 0 {
		t.Fatal("README deployment key-generation command is missing")
	}
	deployment = deployment[start:]
	umask := strings.Index(deployment, "umask 077")
	creation := strings.Index(deployment, "> .env")
	if umask < 0 || creation < 0 || umask > creation {
		t.Fatal("README must set umask 077 before creating .env")
	}
}
