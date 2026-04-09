// Package progress provides a shared progress bar helper.
package progress

import (
	"fmt"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
)

var isTTY = term.IsTerminal(int(os.Stderr.Fd()))

// NewBar creates a progress bar with the standard project style.
// On non-TTY outputs the bar is invisible.
func NewBar(total int, description string) *progressbar.ProgressBar {
	if !isTTY {
		return progressbar.NewOptions(total,
			progressbar.OptionSetVisibility(false),
		)
	}
	return progressbar.NewOptions(total,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWidth(30),
		progressbar.OptionShowCount(),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionThrottle(100*time.Millisecond),
	)
}

// FinishBar completes the bar and prints a summary line with elapsed time.
func FinishBar(bar *progressbar.ProgressBar, description string, detail string, elapsed time.Duration) {
	bar.Finish()
	fmt.Printf("  %s %s in %.1fs\n", description, detail, elapsed.Seconds())
}
