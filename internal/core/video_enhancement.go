package core

// VideoEnhancement is an observable plan/run status, never a promise based
// solely on installed hardware. Only running means enhanced segments have
// actually been produced in the current run. Fallback always carries a reason.
type VideoEnhancement struct {
	Backend string `json:"backend,omitempty"`
	State   string `json:"state,omitempty"` // off, unavailable, ineligible, selected, starting, running, fallback, stopped
	Reason  string `json:"reason,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Quality int    `json:"quality,omitempty"`
}

// VideoEnhancementSupport is detection, not successful execution. Its
// protocols are the transports verified for this backend version.
type VideoEnhancementSupport struct {
	Backend   string   `json:"backend"`
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Version   string   `json:"version,omitempty"`
	Protocols []string `json:"protocols,omitempty"`
	Active    int      `json:"active"`
}
