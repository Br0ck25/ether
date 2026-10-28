package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// Build metadata, set by goreleaser via -ldflags -X. Version is a semver
// string ("1.2.3", optionally "v1.2.3-beta.1"); "dev" in development builds.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""

	// PublicKeyB64 is the base64 (std encoding) ed25519 public key that
	// release manifests are signed with. Injected at release build time;
	// empty in dev builds, in which case Check requires an explicit key.
	PublicKeyB64 = ""
)

// IsDev reports whether this is an unversioned development build.
func IsDev() bool { return Version == "" || Version == "dev" }

// Semver is a parsed semantic version.
type Semver struct {
	Major, Minor, Patch uint64
	Pre                 []string // prerelease identifiers, empty for a release
	raw                 string
}

// ParseVersion parses "v1.2.3-beta.1+build". Missing minor/patch default to
// 0 ("1.2" == 1.2.0). Build metadata is accepted and ignored.
func ParseVersion(s string) (Semver, error) {
	orig := s
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if s == "" {
		return Semver{}, fmt.Errorf("updater: empty version")
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		preStr := s[i+1:]
		s = s[:i]
		if preStr == "" {
			return Semver{}, fmt.Errorf("updater: invalid version %q", orig)
		}
		pre = strings.Split(preStr, ".")
		for _, p := range pre {
			if p == "" {
				return Semver{}, fmt.Errorf("updater: invalid version %q", orig)
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return Semver{}, fmt.Errorf("updater: invalid version %q", orig)
	}
	nums := make([]uint64, 3)
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Semver{}, fmt.Errorf("updater: invalid version %q", orig)
		}
		nums[i] = n
	}
	return Semver{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre, raw: orig}, nil
}

func (v Semver) String() string {
	if v.raw != "" {
		return v.raw
	}
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// CompareVersions returns -1, 0 or 1 (a <, ==, > b) per semver precedence:
// a release outranks its prereleases; numeric prerelease identifiers
// compare numerically and rank below alphanumeric ones.
func CompareVersions(a, b string) (int, error) {
	va, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return va.Compare(vb), nil
}

// Compare is CompareVersions on parsed values.
func (v Semver) Compare(o Semver) int {
	for _, c := range []int{cmpUint(v.Major, o.Major), cmpUint(v.Minor, o.Minor), cmpUint(v.Patch, o.Patch)} {
		if c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := comparePreID(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(v.Pre)), uint64(len(o.Pre)))
}

func comparePreID(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		return cmpUint(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Newer reports whether candidate is strictly newer than current. An
// unparseable/dev current version makes every valid candidate newer.
func Newer(candidate, current string) bool {
	if current == "" || current == "dev" {
		_, err := ParseVersion(candidate)
		return err == nil
	}
	c, err := CompareVersions(candidate, current)
	return err == nil && c > 0
}
