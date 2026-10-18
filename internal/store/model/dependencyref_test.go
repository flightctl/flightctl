package model

import (
	"reflect"
	"testing"
)

func TestStringArrayRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value StringArray
		want  string
	}{
		{
			name:  "empty",
			value: StringArray{},
			want:  "{}",
		},
		{
			name:  "special characters",
			value: StringArray{"team a", "team,b", `quote"name`, "{brace}", `slash\name`, "", "NULL"},
			want:  `{"team a","team,b","quote\"name","{brace}","slash\\name","","NULL"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.value.Value()
			if err != nil {
				t.Fatalf("Value() returned an error: %v", err)
			}
			if encoded != tt.want {
				t.Fatalf("Value() = %q, want %q", encoded, tt.want)
			}

			var decoded StringArray
			if err := decoded.Scan(encoded); err != nil {
				t.Fatalf("Scan() returned an error: %v", err)
			}
			if !reflect.DeepEqual(decoded, tt.value) {
				t.Fatalf("Scan() = %#v, want %#v", decoded, tt.value)
			}
		})
	}
}

func TestStringArrayScanLegacyFormat(t *testing.T) {
	var value StringArray
	if err := value.Scan("{app-config,infra-config}"); err != nil {
		t.Fatalf("Scan() returned an error: %v", err)
	}
	want := StringArray{"app-config", "infra-config"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("Scan() = %#v, want %#v", value, want)
	}
}

func TestStringArrayScanRejectsMalformedInput(t *testing.T) {
	var value StringArray
	if err := value.Scan(`{"unterminated}`); err == nil {
		t.Fatal("Scan() returned nil for malformed input")
	}
}
