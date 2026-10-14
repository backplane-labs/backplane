package wire_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/internal/wire"
)

func TestNATSNames(t *testing.T) {
	t.Parallel()

	for got, want := range map[string]string{
		wire.Token("greeter/templates:iam.X"):                      "greeter_2Ftemplates_3Aiam_2EX",
		wire.StreamName("mail-sender"):                             "bp_mail-sender",
		wire.StreamSubjects("mail-sender"):                         "bp.mail-sender.>",
		wire.Subject("iam", "UserRegistered"):                      "bp.iam.UserRegistered",
		wire.DLQStreamName("mailer"):                               "bp_dlq_mailer",
		wire.DLQStreamSubjects("mailer"):                           "bp.dlq.mailer.>",
		wire.DLQSubject("mailer", "iam.UserRegistered"):            "bp.dlq.mailer.iam_2EUserRegistered",
		wire.Durable("mailer", "send:iam.UserRegistered"):          "mailer__send_3Aiam_2EUserRegistered",
		wire.RedriveSubject("iam", "mailer", "iam.UserRegistered"): "bp.iam._redrive.mailer.iam_2EUserRegistered",
		wire.HooksQueue("iam"):                                     "iam.hooks",
		wire.NexusService("iam"):                                   "iam.Hooks",
		wire.ScheduleID("hello", "Nightly"):                        "hello/Nightly",
		wire.Queue("hello"):                                        "hello",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestUntoken(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"iam", "a.b", "x_y", "greeter/templates:iam.X", "a b*>", "", "ü"} {
		if got, ok := wire.Untoken(wire.Token(s)); !ok || got != s {
			t.Errorf("Untoken(Token(%q)) = %q, %v", s, got, ok)
		}
	}

	for _, bad := range []string{"_", "_2", "_zz", "_redrive", "a_2e"} {
		if _, ok := wire.Untoken(bad); ok {
			t.Errorf("Untoken(%q) is ok", bad)
		}
	}
}

func TestEventOfSubject(t *testing.T) {
	t.Parallel()

	for subject, want := range map[string]string{
		"bp.iam.UserRegistered":    "iam.UserRegistered",
		"bp.a_2Eb.X":               "a.b.X",
		"bp.iam._redrive.mailer.c": "",
		"bp.dlq.mailer.c":          "",
		"other.iam.X":              "",
		"bp.iam":                   "",
	} {
		got, ok := wire.EventOfSubject(subject)
		if ok != (want != "") || got != want {
			t.Errorf("EventOfSubject(%q) = %q, %v; want %q", subject, got, ok, want)
		}
	}
}

func TestSplitEvent(t *testing.T) {
	t.Parallel()

	if s, n, err := wire.SplitEvent("iam.UserRegistered"); err != nil || s != "iam" || n != "UserRegistered" {
		t.Fatalf("SplitEvent: %q %q %v", s, n, err)
	}

	for _, bad := range []string{"", "iam", ".X", "iam."} {
		if _, _, err := wire.SplitEvent(bad); !errors.Is(err, wire.ErrEventName) {
			t.Errorf("SplitEvent(%q): %v", bad, err)
		}
	}
}
