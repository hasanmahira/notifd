package domain

import "fmt"

// Channel is the transport used to deliver a notification.
type Channel string

const (
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
	ChannelPush  Channel = "push"
)

// AllChannels enumerates every valid channel. The rate limiter reads this list.
func AllChannels() []Channel { return []Channel{ChannelSMS, ChannelEmail, ChannelPush} }

func (c Channel) Valid() bool {
	switch c {
	case ChannelSMS, ChannelEmail, ChannelPush:
		return true
	}
	return false
}

func ParseChannel(s string) (Channel, error) {
	c := Channel(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: channel %q", ErrInvalidInput, s)
	}
	return c, nil
}
