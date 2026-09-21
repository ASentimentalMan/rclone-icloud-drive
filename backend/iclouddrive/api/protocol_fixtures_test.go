package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizedProtocolFixturesAreValidJSON(t *testing.T) {
	dir := filepath.Join("..", "testdata", "protocol")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		seen[entry.Name()] = true
	}
	for _, name := range []string{
		"personal-to-shared-move.json",
		"shared-to-personal-move.json",
		"shared-file-reparent.json",
		"shared-folder-reparent.json",
		"shared-create-upload.json",
		"shared-rename-delete.json",
		"recently-deleted.json",
		"personal-file-copy.json",
		"shared-size.json",
	} {
		if !seen[name] {
			t.Errorf("required fixture %q is missing", name)
		}
	}
}

func TestSanitizedProtocolFixturesContainNoRawCaptureHeaders(t *testing.T) {
	dir := filepath.Join("..", "testdata", "protocol")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		assertNoSensitiveFixtureKeys(t, entry.Name(), value)
	}
}

func assertNoSensitiveFixtureKeys(t *testing.T, file string, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch key {
			case "Cookie", "Authorization", "DSID", "X-APPLE-WEBAUTH":
				t.Errorf("%s contains forbidden raw header/account key %q", file, key)
			}
			assertNoSensitiveFixtureKeys(t, file, child)
		}
	case []any:
		for _, child := range value {
			assertNoSensitiveFixtureKeys(t, file, child)
		}
	}
}
