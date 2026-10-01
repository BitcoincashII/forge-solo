package main

import (
	"os"
	"sync"
)

// logFileLimit caps the log file. Past it the file moves to <name>.1, replacing the one before, and
// a new one starts, so the log never holds more than about twice this. A Windows service always logs
// to a file, and nothing else would ever trim it.
var logFileLimit int64 = 20 << 20

type rotatingFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
	size int64
}

func openRotating(path string) (*rotatingFile, error) {
	r := &rotatingFile{path: path}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > logFileLimit {
		r.f.Close()
		renamed := os.Rename(r.path, r.path+".1") == nil
		if err := r.open(); err != nil {
			return 0, err
		}
		if !renamed {
			// Windows refuses the rename while another program has the file open. Keep writing
			// and try again after another limit's worth rather than on every line.
			r.size = 0
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Sync() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Sync()
}
