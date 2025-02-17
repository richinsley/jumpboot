package jumpboot

import (
	"fmt"
	"strings"
)

type Version struct {
	Major int
	Minor int // -1 if not specified
	Patch int // -1 if not specified
}

// ParseVersion parses a version string in the format "X.Y.Z" and returns a Version object.
// If the version string is not in the format "X.Y.Z", it will try parsing it as "X.Y" or "X".
// Any additional characters after the version string will be ignored.
func ParseVersion(versionStr string) (Version, error) {
	version := Version{
		Minor: -1,
		Patch: -1,
	}
	_, err := fmt.Sscanf(versionStr, "%d.%d.%d", &version.Major, &version.Minor, &version.Patch)
	if err != nil {
		// If the version string is not in the format "X.Y.Z", try parsing it as "X.Y"
		_, err = fmt.Sscanf(versionStr, "%d.%d", &version.Major, &version.Minor)
		if err != nil {
			// If the version string is not in the format "X.Y", try parsing it as "X"
			_, err = fmt.Sscanf(versionStr, "%d", &version.Major)
			if err != nil {
				return Version{}, fmt.Errorf("error parsing version: %v", err)
			}
		}
	}
	if version.Major < 0 || version.Minor < -1 || version.Patch < -1 {
		return Version{}, fmt.Errorf("invalid version: %s", versionStr)
	}
	return version, nil
}

func ParsePythonVersion(versionStr string) (Version, error) {
	// split the version string on the space
	// the first part must be "Python", the second part is the version
	parts := strings.Split(versionStr, " ")
	if len(parts) != 2 {
		return Version{}, fmt.Errorf("invalid version string: %s", versionStr)
	}
	if parts[0] != "Python" {
		return Version{}, fmt.Errorf("invalid version string: %s", versionStr)
	}
	return ParseVersion(parts[1])
}

func ParsePipVersion(versionStr string) (Version, error) {
	// split the version string on the space
	// the first part must be "Python", the second part is the version
	parts := strings.Split(versionStr, " ")
	if len(parts) < 2 {
		return Version{}, fmt.Errorf("invalid version string: %s", versionStr)
	}
	if !strings.HasPrefix(parts[0], "pip") {
		return Version{}, fmt.Errorf("invalid version string: %s", versionStr)
	}
	return ParseVersion(parts[1])
}

// Compare compares the version with another version and returns:
// -1 if the version is less than the other version
// 0 if the version is equal to the other version
// 1 if the version is greater than the other version
func (v *Version) Compare(other Version) int {
	if v.Major > other.Major {
		return 1
	}
	if v.Major < other.Major {
		return -1
	}
	if v.Minor > other.Minor {
		return 1
	}
	if v.Minor < other.Minor {
		return -1
	}
	if v.Patch > other.Patch {
		return 1
	}
	if v.Patch < other.Patch {
		return -1
	}
	return 0
}

// String returns the string representation of the version.  We'll ignore -1 values.
func (v *Version) String() string {
	if v.Patch != -1 {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	if v.Minor != -1 {
		return fmt.Sprintf("%d.%d", v.Major, v.Minor)
	}
	return fmt.Sprintf("%d", v.Major)
}

func (v *Version) MinorString() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

func (v *Version) MinorStringCompact() string {
	return fmt.Sprintf("%d%d", v.Major, v.Minor)
}
