package transcode

// rateControl is a run's rate-control plan. The zero value is the historic
// bitrate-target encoding; Quality selects constant-quality encoding
// constrained by a VBV buffer (see vbvRate in session planning).
type rateControl struct {
	Quality     bool
	MaxrateKbps int // VBV peak rate
	BufsizeKbps int // VBV buffer size, in kbit
	TargetKbps  int // nominal target the rung/cap asks for (reporting, HW fallbacks)
}
