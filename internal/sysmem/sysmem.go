// Package sysmem reports how much memory this process is using.
package sysmem

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

// RSS returns the resident set size of the current process in bytes.
//
// On Linux it is read from /proc and is the current figure. Elsewhere (the
// development machine) it falls back to the peak reported by getrusage,
// which is close enough for a readout.
func RSS() int64 {
	if runtime.GOOS == "linux" {
		if n := procRSS("/proc/self/statm"); n > 0 {
			return n
		}
	}
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	peak := int64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		peak *= 1024 // Linux reports kilobytes, macOS bytes
	}
	return peak
}

// procRSS parses the second field of a statm file: resident pages.
func procRSS(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	fields := bytes.Fields(raw)
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// MB converts bytes to whole megabytes, rounding to nearest.
func MB(bytes int64) int {
	return int((bytes + 512*1024) / (1024 * 1024))
}
