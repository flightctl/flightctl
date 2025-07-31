package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSecureString_FormatBehavior(t *testing.T) {
	secret := SecureString("super-secret-password")

	result := secret.String()
	if result != redactedPlaceholder {
		t.Errorf("fmt.Sprintf(%%s) = %v, want %v", result, redactedPlaceholder)
	}

	// Test with fmt.Sprintf using %v, should also use String()
	result = fmt.Sprintf("%v", secret)
	if result != redactedPlaceholder {
		t.Errorf("fmt.Sprintf(%%v) = %v, want %v", result, redactedPlaceholder)
	}

	// Test with fmt.Sprintf using %#v (should use GoString)
	result = fmt.Sprintf("%#v", secret)
	if result != redactedPlaceholder {
		t.Errorf("fmt.Sprintf(%%#v) = %v, want %v", result, redactedPlaceholder)
	}
}

func TestSecureString_JSONMarshaling(t *testing.T) {
	type testStruct struct {
		PublicField string       `json:"public"`
		SecretField SecureString `json:"secret"`
	}

	data := testStruct{
		PublicField: "visible-data",
		SecretField: SecureString("hidden-secret"),
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}

	jsonStr := string(jsonBytes)
	expectedJSON := `{"public":"visible-data","secret":"` + redactedPlaceholder + `"}`

	if jsonStr != expectedJSON {
		t.Errorf("JSON marshaling = %v, want %v", jsonStr, expectedJSON)
	}

	if strings.Contains(jsonStr, "hidden-secret") {
		t.Error("Secret value found in JSON output")
	}
}
