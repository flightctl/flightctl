package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/quadlet/renderer"
)

func TestCompleteConfig_AAPClientID(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-render-template-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockFile := filepath.Join(tmpDir, "aap-client-id")
	if err := os.WriteFile(mockFile, []byte("file-client-id"), 0600); err != nil {
		t.Fatalf("failed to write mock client id file: %v", err)
	}

	// Override DefaultAAPClientIDPath
	origPath := renderer.DefaultAAPClientIDPath
	renderer.DefaultAAPClientIDPath = mockFile
	defer func() {
		renderer.DefaultAAPClientIDPath = origPath
	}()

	tests := []struct {
		name         string
		inputData    map[string]interface{}
		wantClientID string
	}{
		{
			name: "uses file client id when config is empty",
			inputData: map[string]interface{}{
				"global": map[string]interface{}{
					"baseDomain": "example.com",
					"auth": map[string]interface{}{
						"type": "aap",
					},
				},
			},
			wantClientID: "file-client-id",
		},
		{
			name: "prioritizes config client id over file",
			inputData: map[string]interface{}{
				"global": map[string]interface{}{
					"baseDomain": "example.com",
					"auth": map[string]interface{}{
						"type": "aap",
						"aap": map[string]interface{}{
							"clientId": "config-client-id",
						},
					},
				},
			},
			wantClientID: "config-client-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &RenderTemplateOptions{}
			err := opts.completeConfig(tt.inputData)
			if err != nil {
				t.Fatalf("completeConfig failed: %v", err)
			}

			global, ok := tt.inputData["global"].(map[string]interface{})
			if !ok {
				t.Fatalf("global missing")
			}
			auth, ok := global["auth"].(map[string]interface{})
			if !ok {
				t.Fatalf("auth missing")
			}
			aap, ok := auth["aap"].(map[string]interface{})
			if !ok {
				t.Fatalf("aap missing")
			}

			gotClientID, _ := aap["clientId"].(string)
			if gotClientID != tt.wantClientID {
				t.Errorf("got clientId %q, want %q", gotClientID, tt.wantClientID)
			}
		})
	}
}
