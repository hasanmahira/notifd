package domain

import (
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"unicode"
)

// Recipient is a channel-specific address. We keep it as a string but give it a
// LogValue so slog redacts PII automatically everywhere Recipient flows.
type Recipient string

// Validate checks channel-specific addressing rules.
func (r Recipient) Validate(c Channel) error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: recipient empty", ErrInvalidInput)
	}
	switch c {
	case ChannelSMS:
		if !isPhoneNumber(s) {
			return fmt.Errorf("%w: recipient must be E.164 phone", ErrInvalidInput)
		}
	case ChannelEmail:
		if _, err := mail.ParseAddress(s); err != nil {
			return fmt.Errorf("%w: recipient must be valid email: %s", ErrInvalidInput, err.Error())
		}
	case ChannelPush:
		if len(s) < 8 || len(s) > 4096 {
			return fmt.Errorf("%w: recipient push token length out of range", ErrInvalidInput)
		}
	}
	return nil
}

// LogValue implements slog.LogValuer so recipients render as masked strings in logs.
// "+905551234567" -> "+90***4567"
// "user@example.com" -> "u***@example.com"
// Long push tokens -> "push:<first4>...<last4>"
func (r Recipient) LogValue() slog.Value {
	s := string(r)
	if s == "" {
		return slog.StringValue("")
	}
	if strings.HasPrefix(s, "+") {
		if len(s) <= 7 {
			return slog.StringValue(s[:1] + "***")
		}
		return slog.StringValue(s[:3] + "***" + s[len(s)-4:])
	}
	if at := strings.IndexByte(s, '@'); at > 0 {
		if at == 1 {
			return slog.StringValue(s[:1] + "***" + s[at:])
		}
		return slog.StringValue(string(s[0]) + "***" + s[at:])
	}
	// treat as push token
	if len(s) <= 10 {
		return slog.StringValue("push:***")
	}
	return slog.StringValue("push:" + s[:4] + "..." + s[len(s)-4:])
}

func isPhoneNumber(s string) bool {
	if len(s) < 8 || len(s) > 20 {
		return false
	}
	if s[0] != '+' {
		return false
	}
	for _, r := range s[1:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
