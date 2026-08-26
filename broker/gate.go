package main

// Paste gate refusal reasons (muxa#133). Empty means free to paste.
const (
	gatePaneDead        = "pane-dead"
	gateInMode          = "in-mode"
	gateDrawing         = "drawing"
	gateTwoSignal       = "two-signal"
	gateForeignComposer = "foreign-composer"
	gateNoDraw          = "no-draw"
	gateTmuxError       = "tmux-error"
)

// HeldEntry is one pending message the broker refused to paste on its last
// tick, surfaced in muxa broker status (muxa#133). Older clients ignore
// unknown JSON fields on the status response.
type HeldEntry struct {
	ID       string `json:"id"`
	To       string `json:"to"`
	Pane     string `json:"pane"`
	AgeSec   int64  `json:"age"`
	Reason   string `json:"reason"`
	Refusals int    `json:"refusals"`
	Attempts int    `json:"attempts"`
}

func gateOr(got, fallback string) string {
	if got != "" {
		return got
	}
	return fallback
}
