package config_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

func TestCheckInboxPrefix(t *testing.T) {
	t.Parallel()

	for prefix, ok := range map[string]bool{
		"":                  true,
		"_INBOX_billing":    true,
		"_INBOX_billing.v1": true,
		"_INBOX.>":          false,
		"_INBOX_*":          false,
		"_INBOX_billing.":   false,
		".billing":          false,
		"_INBOX..billing":   false,
		"_INBOX billing":    false,
		"_INBOX\nbilling":   false,
	} {
		err := config.CheckInboxPrefix(prefix)
		if ok != (err == nil) || (!ok && !errors.Is(err, config.ErrInboxPrefix)) {
			t.Errorf("%q: %v", prefix, err)
		}
	}
}
