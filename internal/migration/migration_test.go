package migration

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name             string
		enrollmentSigner string
		wantErr          string
	}{
		{
			name:    "When the enrollment signer is empty it should reject the migration",
			wantErr: "enrollment signer name is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Run(context.Background(), nil, logrus.New(), false, test.enrollmentSigner)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}
