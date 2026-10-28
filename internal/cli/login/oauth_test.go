package login

import (
	"errors"
	"testing"

	"github.com/openshift/osincli"
	"github.com/stretchr/testify/require"
)

func TestOAuth2AuthCodeFlowCallback(t *testing.T) {
	t.Run("When using the shared browser flow it should preserve the localhost callback", func(t *testing.T) {
		stop := errors.New("stop before opening the browser")
		var callback string
		_, err := oauth2AuthCodeFlow(func(callbackURL string) (*osincli.Client, error) {
			callback = callbackURL
			return nil, stop
		}, 0, true)

		require.ErrorIs(t, err, stop)
		require.Equal(t, "http://localhost:0/callback", callback)
	})
}
