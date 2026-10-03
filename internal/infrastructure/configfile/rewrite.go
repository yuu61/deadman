package configfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type rewriteSource struct {
	path string
	info fs.FileInfo
	data []byte
}

// replacementFile is the temporary file whose lifecycle must finish before commit.
type replacementFile interface {
	Name() string
	WriteString(text string) (int, error)
	Chmod(mode fs.FileMode) error
	Sync() error
	Close() error
}

// Rewrite replaces a configuration only if it still matches the validated input.
// The replacement is fully written, synced and closed in the same directory before
// renaming it over the source. Symbolic links are followed without replacing them.
func Rewrite(path string, original []byte, formatted string) error {
	source, err := readRewriteSource(path)
	if err != nil {
		return err
	}

	if !bytes.Equal(source.data, original) {
		return fmt.Errorf("%s: configuration changed while formatting", path)
	}

	if bytes.Equal(source.data, []byte(formatted)) {
		return nil
	}

	if source.info.Mode().Perm()&0o222 == 0 {
		return fmt.Errorf("%s: configuration is read-only", path)
	}

	file, err := os.CreateTemp(filepath.Dir(source.path), ".deadman-format-*")
	if err != nil {
		return err
	}

	return finishRewrite(path, file, formatted, source)
}

func finishRewrite(
	path string,
	file replacementFile,
	formatted string,
	source rewriteSource,
) error {
	err := writeReplacement(file, formatted, source.info.Mode().Perm())
	if err == nil {
		err = commitReplacement(path, file.Name(), source)
	}

	return errors.Join(err, removeReplacement(file.Name()))
}

func readRewriteSource(path string) (rewriteSource, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return rewriteSource{}, fmt.Errorf("%s: resolve configuration path: %w", path, err)
	}

	file, err := os.Open(resolved)
	if err != nil {
		return rewriteSource{}, err
	}

	info, err := file.Stat()
	if err != nil {
		return rewriteSource{}, errors.Join(err, file.Close())
	}

	if !info.Mode().IsRegular() {
		return rewriteSource{}, errors.Join(
			fmt.Errorf("%s: configuration must be a regular file", path), file.Close(),
		)
	}

	data, readErr := io.ReadAll(file)

	err = errors.Join(readErr, file.Close())
	if err != nil {
		return rewriteSource{}, fmt.Errorf("%s: read configuration before replacing: %w", path, err)
	}

	return rewriteSource{path: resolved, info: info, data: data}, nil
}

func writeReplacement(file replacementFile, formatted string, mode fs.FileMode) error {
	err := prepareReplacement(file, formatted, mode)

	return errors.Join(err, file.Close())
}

func prepareReplacement(file replacementFile, formatted string, mode fs.FileMode) error {
	_, err := file.WriteString(formatted)
	if err != nil {
		return fmt.Errorf("write formatted configuration: %w", err)
	}

	err = file.Chmod(mode)
	if err != nil {
		return err
	}

	return file.Sync()
}

func commitReplacement(path, temporary string, source rewriteSource) error {
	current, err := readRewriteSource(path)
	if err != nil {
		return err
	}

	if current.path != source.path || !os.SameFile(current.info, source.info) ||
		current.info.Mode() != source.info.Mode() || !bytes.Equal(current.data, source.data) {
		return fmt.Errorf("%s: configuration changed while formatting", path)
	}

	return os.Rename(temporary, source.path)
}

func removeReplacement(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}
