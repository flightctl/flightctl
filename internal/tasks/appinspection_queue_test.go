package tasks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDecodeInspectionWakeup(t *testing.T) {
	message := inspectionWakeup{Kind: inspectionWork, OrgID: uuid.Nil, ID: uuid.New()}
	body, err := json.Marshal(message)
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"the default organization is explicit it should accept the wakeup", string(body), true},
		{"the organization is missing it should reject the wakeup", strings.ReplaceAll(string(body), `"orgID":"`+uuid.Nil.String()+`",`, ""), false},
		{"a message includes application data it should reject the wakeup", strings.TrimSuffix(string(body), "}") + `,"configuration":{}}`, false},
		{"a message is not an inspection kind it should reject the wakeup", strings.ReplaceAll(string(body), `"work"`, `"event"`), false},
		{"a payload contains multiple messages it should reject the wakeup", string(body) + string(body), false},
	} {
		t.Run("When "+tc.name, func(t *testing.T) {
			decoded, err := decodeInspectionWakeup([]byte(tc.body))
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, message, decoded)
			} else {
				require.Error(t, err)
			}
		})
	}
}
