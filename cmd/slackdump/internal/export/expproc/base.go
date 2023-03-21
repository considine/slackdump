package expproc

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rusq/slackdump/v2/internal/chunk"
)

type baseproc struct {
	dir string
	wf  io.Closer // processor recording
	gz  io.WriteCloser
	*chunk.Recorder
}

func newBaseProc(dir string, name string) (*baseproc, error) {
	if fi, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dir)
	}
	filename := filepath.Join(dir, name+ext)
	if fi, err := os.Stat(filename); err == nil {
		if fi.IsDir() {
			return nil, fmt.Errorf("not a file: %s", filename)
		}
		if fi.Size() > 0 {
			runtime.Breakpoint()
			return nil, fmt.Errorf("file %s exists and not empty", filename)
		}
	}
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(f)
	r := chunk.NewRecorder(gz)
	return &baseproc{dir: dir, wf: f, gz: gz, Recorder: r}, nil
}

func (p *baseproc) Close() error {
	if err := p.Recorder.Close(); err != nil {
		p.gz.Close()
		p.wf.Close()
		return err
	}
	if err := p.gz.Close(); err != nil {
		p.wf.Close()
		return err
	}
	return p.wf.Close()
}
