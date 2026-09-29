package domain

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Validation caps per channel. These mirror this project's product spec.
const (
	MaxSMSChars        = 1600
	MaxEmailSubject    = 255
	MaxPushPayloadSize = 4096
	MaxBatchSize       = 1000
)

// smsBody / emailBody / pushBody mirror the expected content shape per channel.
// Content is stored as opaque JSONB; these types are only used during validation.
type smsBody struct {
	Text string `json:"text"`
}

type emailBody struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	HTML    string `json:"html,omitempty"`
}

type pushBody struct {
	Title string         `json:"title"`
	Body  string         `json:"body"`
	Data  map[string]any `json:"data,omitempty"`
}

// ValidateContent checks per-channel content shape and size. Returns a
// wrapped ErrInvalidInput on violations.
func ValidateContent(c Channel, content Content) error {
	if len(content) == 0 {
		return fmt.Errorf("%w: content empty", ErrInvalidInput)
	}
	raw := json.RawMessage(content)
	switch c {
	case ChannelSMS:
		var b smsBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("%w: sms content not JSON {text}: %s", ErrInvalidInput, err.Error())
		}
		if b.Text == "" {
			return fmt.Errorf("%w: sms content.text empty", ErrInvalidInput)
		}
		if utf8.RuneCountInString(b.Text) > MaxSMSChars {
			return fmt.Errorf("%w: sms content.text exceeds %d chars", ErrInvalidInput, MaxSMSChars)
		}
	case ChannelEmail:
		var b emailBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("%w: email content not JSON {subject,body,html?}: %s", ErrInvalidInput, err.Error())
		}
		if b.Subject == "" || b.Body == "" {
			return fmt.Errorf("%w: email requires non-empty subject and body", ErrInvalidInput)
		}
		if utf8.RuneCountInString(b.Subject) > MaxEmailSubject {
			return fmt.Errorf("%w: email subject exceeds %d chars", ErrInvalidInput, MaxEmailSubject)
		}
	case ChannelPush:
		var b pushBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("%w: push content not JSON {title,body,data?}: %s", ErrInvalidInput, err.Error())
		}
		if b.Title == "" || b.Body == "" {
			return fmt.Errorf("%w: push requires non-empty title and body", ErrInvalidInput)
		}
		if len(raw) > MaxPushPayloadSize {
			return fmt.Errorf("%w: push payload exceeds %d bytes", ErrInvalidInput, MaxPushPayloadSize)
		}
	default:
		return fmt.Errorf("%w: unknown channel %q", ErrInvalidInput, c)
	}
	return nil
}
