package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

type Logger struct {
	*log.Logger
}

func setupLogger(baseDir, logFile string, console bool) (*Logger, error) {
	path := filepath.Join(baseDir, logFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	var w io.Writer = f
	if console {
		w = io.MultiWriter(f, os.Stdout)
	}
	return &Logger{Logger: log.New(w, "", log.LstdFlags)}, nil
}
