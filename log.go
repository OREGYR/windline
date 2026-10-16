package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// log.go writes a small rolling log to %LOCALAPPDATA%\Windline\windline.log.
//
// A GUI app has no console, so this is the only way to see what happened: the
// test harness reads it after each run, and a user reporting a bug can too. It
// never grows beyond a few hundred kilobytes.

var (
	logMu   sync.Mutex
	logFile *os.File
	logPath string
	logOn   = true
)

func logStart() {
	logPath = filepath.Join(localDataDir(), "windline.log")
	// Roll once at startup so a long-running install cannot grow without bound.
	if info, err := os.Stat(logPath); err == nil && info.Size() > 512*1024 {
		os.Remove(logPath + ".old")
		os.Rename(logPath, logPath+".old")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logOn = false
		return
	}
	logFile = f
	logf("--- windline starting, pid %d ---", os.Getpid())
}

func logStop() {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
	logOn = false
}

// logf appends a timestamped line.
func logf(format string, args ...any) {
	if !logOn {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	if logFile == nil {
		return
	}
	stamp := time.Now().Format("15:04:05.000")
	fmt.Fprintf(logFile, "%s %s\n", stamp, fmt.Sprintf(format, args...))
	logFile.Sync()
}

// logPathFor returns the log's location, used by the CLI's --log switch.
func logPathFor() string { return logPath }
