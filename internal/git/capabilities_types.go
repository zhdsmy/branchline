package git

// GitVersion is the numeric release reported by the Git executable belonging
// to a Repo's executor. It must never be inferred from the local executable
// when the repository is on SSH. Vendor suffixes remain in RawVersion below.
type GitVersion struct {
	Major int
	Minor int
	Patch int
}

// GitCapabilities is an immutable value returned by Repo.Capabilities.
// A value is valid only when that call succeeds; a detection failure must not
// be presented as a verified old version or as support for every feature.
// Keep request ownership, caching, and UI warnings in the caller.
type GitCapabilities struct {
	Version    GitVersion
	RawVersion string
	// MeetsMinimum means Git >= 2.25. Opening older repositories is allowed,
	// with a visible compatibility warning.
	MeetsMinimum bool
	// Optional flags remain unavailable below their introduction release.
	RebaseUpdateRefs  bool // Git >= 2.38
	StashStaged       bool // Git >= 2.35
	InitInitialBranch bool // Git >= 2.28; older Git needs an init fallback.
}

// GitCapabilityReader is the shared read contract for capability consumers.
// Implementations must query the captured repository executor, return errors
// for failed/unparseable detection, and perform no persistent configuration
// changes. Calling this interface is a background operation, never rendering.
type GitCapabilityReader interface {
	Capabilities() (GitCapabilities, error)
}
