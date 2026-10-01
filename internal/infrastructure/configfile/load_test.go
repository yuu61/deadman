package configfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Load parses the file at path like Parse.
func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deadman.conf")

	err := os.WriteFile(path, []byte("scale 5\nh 1.2.3.4 probe=ssh relay=jump os=Linux\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Display.Scale != 5 || len(cfg.Lines) != 1 || target(t, cfg, 0).Name != "h" ||
		params[probe.SSH](t, target(t, cfg, 0).Params).Host != "jump" {
		t.Errorf("Load = %+v, want scale 5 and the relay target h", cfg)
	}
}

// A file that cannot be opened reports the open error as it is — it names the path and
// is a not-exist error — since the CLI and a failed reload show it to the operator.
func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.conf")

	_, err := Load(t.Context(), path)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Errorf("Load(missing) error = %v, want a not-exist error naming %s", err, path)
	}
}
