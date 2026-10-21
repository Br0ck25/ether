package config

// RTXVideo controls the optional, owner-installed NVEncC RTX Video backend.
// Empty mode is off. Paths are node-local and changes require a restart.
// Numeric zero values use the documented defaults; invalid values disable
// enhancement rather than silently substituting a non-AI scaler.
type RTXVideo struct {
	Mode            string `json:"mode"`            // off, auto (LAN), on (also remote)
	ExecutablePath  string `json:"executablePath"`  // optional NVEncC64.exe, otherwise PATH
	SourceMaxHeight int    `json:"sourceMaxHeight"` // default 720
	TargetMaxHeight int    `json:"targetMaxHeight"` // default 1080, maximum 2160
	Quality         int    `json:"quality"`         // default 2, NGX quality 1..4 or streaming 21/23 (Ampere+); 0 uses default
	MaxConcurrent   int    `json:"maxConcurrent"`   // default 1, maximum 8
}
