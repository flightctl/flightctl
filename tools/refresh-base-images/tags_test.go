package main

import (
	"testing"
)

func TestParseUBITag(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		wantMajor int
		wantMinor int
		wantTS    int
		wantErr   bool
	}{
		{
			name:      "When given a valid el9 tag it should parse correctly",
			tag:       "9.7-1762965531",
			wantMajor: 9,
			wantMinor: 7,
			wantTS:    1762965531,
		},
		{
			name:      "When given a valid el10 tag it should parse correctly",
			tag:       "10.1-1769518576",
			wantMajor: 10,
			wantMinor: 1,
			wantTS:    1769518576,
		},
		{
			name:    "When given a tag without timestamp it should return an error",
			tag:     "9.7",
			wantErr: true,
		},
		{
			name:    "When given an empty string it should return an error",
			tag:     "",
			wantErr: true,
		},
		{
			name:    "When given a tag with non-numeric major it should return an error",
			tag:     "abc.7-123",
			wantErr: true,
		},
		{
			name:    "When given a go-toolset style tag it should return an error",
			tag:     "1.26.7-1787774815",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUBITag(tt.tag)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseUBITag(%q) error = %v, wantErr %v", tt.tag, err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if got.Major != tt.wantMajor || got.Minor != tt.wantMinor || got.Timestamp != tt.wantTS {
				t.Errorf("ParseUBITag(%q) = {Major:%d, Minor:%d, Timestamp:%d}, want {Major:%d, Minor:%d, Timestamp:%d}",
					tt.tag, got.Major, got.Minor, got.Timestamp, tt.wantMajor, tt.wantMinor, tt.wantTS)
			}
			if got.Raw != tt.tag {
				t.Errorf("ParseUBITag(%q).Raw = %q", tt.tag, got.Raw)
			}
		})
	}
}

func TestParseGoToolsetTag(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		wantMajor int
		wantMinor int
		wantPatch int
		wantTS    int
		wantErr   bool
	}{
		{
			name:      "When given a valid go-toolset tag it should parse correctly",
			tag:       "1.26.7-1787774815",
			wantMajor: 1,
			wantMinor: 26,
			wantPatch: 7,
			wantTS:    1787774815,
		},
		{
			name:    "When given a UBI-style tag it should return an error",
			tag:     "9.7-1762965531",
			wantErr: true,
		},
		{
			name:    "When given an empty string it should return an error",
			tag:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGoToolsetTag(tt.tag)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseGoToolsetTag(%q) error = %v, wantErr %v", tt.tag, err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if got.Major != tt.wantMajor || got.Minor != tt.wantMinor || got.Patch != tt.wantPatch || got.Timestamp != tt.wantTS {
				t.Errorf("ParseGoToolsetTag(%q) = {%d.%d.%d-%d}, want {%d.%d.%d-%d}",
					tt.tag, got.Major, got.Minor, got.Patch, got.Timestamp,
					tt.wantMajor, tt.wantMinor, tt.wantPatch, tt.wantTS)
			}
		})
	}
}

func TestUBITagPattern(t *testing.T) {
	tests := []struct {
		name    string
		major   string
		input   string
		matches bool
	}{
		{"When EL9 pattern matches EL9 tag it should match", "9", "9.7-1762965531", true},
		{"When EL9 pattern is checked against EL10 tag it should not match", "9", "10.1-1769518576", false},
		{"When EL10 pattern matches EL10 tag it should match", "10", "10.1-1769518576", true},
		{"When EL9 pattern is checked against go-toolset tag it should not match", "9", "1.26.7-1787774815", false},
		{"When EL9 pattern is checked against non-timestamped tag it should not match", "9", "9.7", false},
		{"When EL9 pattern is checked against latest tag it should not match", "9", "latest", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := UBITagPattern(tt.major)
			if got := pattern.MatchString(tt.input); got != tt.matches {
				t.Errorf("UBITagPattern(%q).MatchString(%q) = %v, want %v", tt.major, tt.input, got, tt.matches)
			}
		})
	}
}

func TestGoToolsetTagPattern(t *testing.T) {
	tests := []struct {
		name    string
		goMinor string
		input   string
		matches bool
	}{
		{"When 1.26 pattern matches 1.26.x tag it should match", "1.26", "1.26.7-1787774815", true},
		{"When 1.26 pattern is checked against 1.25.x tag it should not match", "1.26", "1.25.9-1234567890", false},
		{"When 1.26 pattern is checked against UBI tag it should not match", "1.26", "9.7-1762965531", false},
		{"When 1.26 pattern is checked against 1.27.x tag it should not match", "1.26", "1.27.0-1234567890", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := GoToolsetTagPattern(tt.goMinor)
			if got := pattern.MatchString(tt.input); got != tt.matches {
				t.Errorf("GoToolsetTagPattern(%q).MatchString(%q) = %v, want %v", tt.goMinor, tt.input, got, tt.matches)
			}
		})
	}
}

func TestLatestMatchingTag(t *testing.T) {
	tests := []struct {
		name    string
		tags    []string
		major   string
		want    string
	}{
		{
			name:  "When multiple EL9 tags exist it should return the one with highest timestamp",
			tags:  []string{"9.5-1700000000", "9.7-1762965531", "9.6-1750000000", "latest", "9.7"},
			major: "9",
			want:  "9.7-1762965531",
		},
		{
			name:  "When no matching tags exist it should return empty string",
			tags:  []string{"latest", "10.1-1769518576"},
			major: "9",
			want:  "",
		},
		{
			name:  "When tags list is empty it should return empty string",
			tags:  []string{},
			major: "9",
			want:  "",
		},
		{
			name:  "When single matching tag exists it should return it",
			tags:  []string{"10.1-1769518576"},
			major: "10",
			want:  "10.1-1769518576",
		},
		{
			name:  "When tags have same version but different timestamps it should pick latest",
			tags:  []string{"9.7-1000000000", "9.7-2000000000", "9.7-1500000000"},
			major: "9",
			want:  "9.7-2000000000",
		},
		{
			name:  "When higher minor version has lower timestamp it should prefer higher minor",
			tags:  []string{"9.5-2000000000", "9.7-1000000000", "9.6-1500000000"},
			major: "9",
			want:  "9.7-1000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := UBITagPattern(tt.major)
			got := LatestMatchingTag(tt.tags, pattern)
			if got != tt.want {
				t.Errorf("LatestMatchingTag(%v, %q) = %q, want %q", tt.tags, tt.major, got, tt.want)
			}
		})
	}
}

func TestExtractGoMinorVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{
			name:    "When go.mod has toolchain directive it should extract minor version",
			content: "module example.com/foo\n\ngo 1.26.0\n\ntoolchain go1.26.7\n",
			want:    "1.26",
		},
		{
			name:    "When go.mod has no toolchain directive it should return an error",
			content: "module example.com/foo\n\ngo 1.26.0\n",
			wantErr: true,
		},
		{
			name:    "When content is empty it should return an error",
			content: "",
			wantErr: true,
		},
		{
			name:    "When toolchain has different version it should extract correctly",
			content: "module x\n\ngo 1.25.0\n\ntoolchain go1.25.9\n",
			want:    "1.25",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractGoMinorVersion(tt.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractGoMinorVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractGoMinorVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
