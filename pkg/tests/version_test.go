package pkg_test

import (
	"testing"

	jumpboot "github.com/richinsley/jumpboot/pkg"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    jumpboot.Version
		expectError bool
	}{
		{"Full jumpboot.Version", "1.2.3", jumpboot.Version{1, 2, 3}, false},
		{"Major and minor", "1.2", jumpboot.Version{1, 2, -1}, false},
		{"Major only", "1", jumpboot.Version{1, -1, -1}, false},
		{"Non-numeric", "a.b.c", jumpboot.Version{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := jumpboot.ParseVersion(tt.input)
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error, but got nil %v", tt.name)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result != tt.expected {
					t.Errorf("Expected %v, but got %v", tt.expected, result)
				}
			}
		})
	}
}

func TestParsePythonVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    jumpboot.Version
		expectError bool
	}{
		{"Valid Python jumpboot.Version", "Python 3.8.5", jumpboot.Version{3, 8, 5}, false},
		{"Invalid prefix", "python 3.8.5", jumpboot.Version{}, true},
		{"Missing jumpboot.Version", "Python", jumpboot.Version{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := jumpboot.ParsePythonVersion(tt.input)
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error, but got nil (%v)", tt.name)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result != tt.expected {
					t.Errorf("Expected %v, but got %v", tt.expected, result)
				}
			}
		})
	}
}

func TestParsePipVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    jumpboot.Version
		expectError bool
	}{
		{"Valid pip jumpboot.Version", "pip 20.2.3", jumpboot.Version{20, 2, 3}, false},
		{"Valid pip jumpboot.Version with extra info", "pip 20.2.3 from /usr/local/lib/python3.8/site-packages/pip (python 3.8)", jumpboot.Version{20, 2, 3}, false},
		{"Invalid prefix", "python-pip 20.2.3", jumpboot.Version{}, true},
		{"Missing jumpboot.Version", "pip", jumpboot.Version{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := jumpboot.ParsePipVersion(tt.input)
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error, but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result != tt.expected {
					t.Errorf("Expected %v, but got %v", tt.expected, result)
				}
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		name     string
		v1       jumpboot.Version
		v2       jumpboot.Version
		expected int
	}{
		{"Equal", jumpboot.Version{1, 2, 3}, jumpboot.Version{1, 2, 3}, 0},
		{"Greater major", jumpboot.Version{2, 0, 0}, jumpboot.Version{1, 9, 9}, 1},
		{"Less major", jumpboot.Version{1, 9, 9}, jumpboot.Version{2, 0, 0}, -1},
		{"Greater minor", jumpboot.Version{1, 2, 0}, jumpboot.Version{1, 1, 9}, 1},
		{"Less minor", jumpboot.Version{1, 1, 9}, jumpboot.Version{1, 2, 0}, -1},
		{"Greater patch", jumpboot.Version{1, 1, 2}, jumpboot.Version{1, 1, 1}, 1},
		{"Less patch", jumpboot.Version{1, 1, 1}, jumpboot.Version{1, 1, 2}, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.v1.Compare(tt.v2)
			if result != tt.expected {
				t.Errorf("Expected %d, but got %d", tt.expected, result)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	tests := []struct {
		name     string
		version  jumpboot.Version
		expected string
	}{
		{"Full jumpboot.Version", jumpboot.Version{1, 2, 3}, "1.2.3"},
		{"Major and minor", jumpboot.Version{1, 2, -1}, "1.2"},
		{"Major only", jumpboot.Version{1, -1, -1}, "1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.version.String()
			if result != tt.expected {
				t.Errorf("Expected %s, but got %s", tt.expected, result)
			}
		})
	}
}

func TestVersionMinorString(t *testing.T) {
	tests := []struct {
		name     string
		version  jumpboot.Version
		expected string
	}{
		{"Full jumpboot.Version", jumpboot.Version{1, 2, 3}, "1.2"},
		{"Major and minor", jumpboot.Version{1, 2, -1}, "1.2"},
		{"Major only", jumpboot.Version{1, -1, -1}, "1.-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.version.MinorString()
			if result != tt.expected {
				t.Errorf("Expected %s, but got %s", tt.expected, result)
			}
		})
	}
}

func TestVersionMinorStringCompact(t *testing.T) {
	tests := []struct {
		name     string
		version  jumpboot.Version
		expected string
	}{
		{"Full jumpboot.Version", jumpboot.Version{1, 2, 3}, "12"},
		{"Major and minor", jumpboot.Version{1, 2, -1}, "12"},
		{"Major only", jumpboot.Version{1, -1, -1}, "1-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.version.MinorStringCompact()
			if result != tt.expected {
				t.Errorf("Expected %s, but got %s", tt.expected, result)
			}
		})
	}
}
