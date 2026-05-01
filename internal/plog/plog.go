// Package plog is a tiny timestamped logger for pipeline stages.
//
// All pipeline-stage console output flows through plog so the user
// can see wall-clock costs and spot duplicate work (cache misses)
// by comparing timestamps across runs. Lines look like:
//
//	[18:35:12.345] Parsing /path/to/model.glb...
//	[18:35:13.987] Alpha-wrap: alpha=0.400 mm, offset=0.013 mm starting
//	[18:40:25.612] Alpha-wrap: 1761586 vertices, 3524832 faces in 311.6s
//
// plog deliberately does not depend on the standard log package: the
// pipeline already writes to stdout (Wails captures stdout for the
// dev terminal), and we want the timestamp prefix only — no file/line
// or other log.Lflags noise.
package plog

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var mu sync.Mutex

// Printf writes a single timestamped line. The format string must not
// include a trailing newline — Printf always appends one. Multiple
// goroutines may call Printf concurrently; output is line-atomic.
func Printf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(os.Stdout, "[%s] %s\n",
		time.Now().Format("15:04:05.000"), msg)
}

// Println writes a single timestamped line containing the given args
// joined by spaces (matches fmt.Println's behavior). Always appends a
// newline.
func Println(args ...any) {
	msg := fmt.Sprintln(args...)
	// fmt.Sprintln appends a newline; strip it because Printf adds one.
	msg = msg[:len(msg)-1]
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(os.Stdout, "[%s] %s\n",
		time.Now().Format("15:04:05.000"), msg)
}
