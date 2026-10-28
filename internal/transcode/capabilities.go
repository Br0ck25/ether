package transcode

// RTXRunner is a package-specific optional capability.
type RTXRunner interface {
	StartRTX(Command) (Process, error)
}
