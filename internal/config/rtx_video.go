package config

// RTXVideo controls the optional NVEncC RTX Video backend.
// Empty mode is off. Enabling installs the pinned runtime on supported Windows
// nodes when no executable is configured. Paths remain node-local.
// Numeric zero values use the documented defaults; invalid values disable
// enhancement rather than silently substituting a non-AI scaler.
type RTXVideo struct {
	Mode            string `json:"mode"`            // off, auto (LAN), on (also remote)
	ExecutablePath  string `json:"executablePath"`  // optional NVEncC64.exe, otherwise managed runtime or PATH
	SourceMaxHeight int    `json:"sourceMaxHeight"` // default 720
	TargetMaxHeight int    `json:"targetMaxHeight"` // default 1080, maximum 2160
	Quality         int    `json:"quality"`         // default 2, NGX quality 1..4 or streaming 21/23 (Ampere+); 0 uses default
	MaxConcurrent   int    `json:"maxConcurrent"`   // default 1, maximum 8
}
