package fsadapter

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var _ FS = Directory{}

type Directory struct {
	dir string
}

func NewDirectory(dir string) Directory {
	return Directory{dir: dir}
}

func (d Directory) String() string {
	return "<directory: " + d.dir + ">"
}

func (fs Directory) Create(fpath string) (io.WriteCloser, error) {
	node := filepath.Join(fs.dir, fpath)
	if err := fs.ensureSubdir(node); err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", node, err)
	}
	nodeDir := filepath.Dir(node)
	if err := mkdirAll(nodeDir); err != nil {
		return nil, err
	}
	return os.Create(node)
}

var ErrIllegalDir = errors.New("illegal file path reference outside of working directory")

// ensureSubdir ensures that the node is a subdirectory of
// the fs.dir, and returns ErrIllegalDir, if not.  This ensures
// that caller won't be able to do anything naughty.
func (fs Directory) ensureSubdir(node string) error {
	if rel, err := filepath.Rel(fs.dir, node); err != nil {
		return err
	} else if strings.HasPrefix(rel, "..") {
		return ErrIllegalDir
	}

	return nil
}

// mkdirAll creates a directory "name", if the directory exists, it does nothing.
func mkdirAll(name string) error {
	if name == "" {
		return errors.New("empty directory")
	}

	fi, err := os.Stat(name)
	if err == nil && fi.IsDir() {
		// exists and is a directory
		return nil
	}

	if err := os.MkdirAll(name, 0755); err != nil {
		return err
	}
	return nil
}

func (fs Directory) WriteFile(name string, data []byte, perm os.FileMode) error {
	node := filepath.Join(fs.dir, name)
	if err := fs.ensureSubdir(node); err != nil {
		return fmt.Errorf("WriteFile: %w", err)
	}
	if err := mkdirAll(filepath.Dir(node)); err != nil {
		return err
	}
	return os.WriteFile(node, data, perm)
}

func (fs Directory) Close() error {
	return nil
}
