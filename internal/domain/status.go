package domain

// Status is the lifecycle state of a single user_notifications row.
//
// Transitions are enforced at the service layer:
//
//	pending  -> processing | canceled
//	processing -> sent | failed | dead_letter | (back to pending on rate-limit backpressure)
//	sent     -> (terminal)
//	failed   -> (terminal)
//	canceled -> (terminal)
//	dead_letter -> (terminal; replay brings it back to pending)
type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusSent       Status = "sent"
	StatusFailed     Status = "failed"
	StatusCanceled   Status = "canceled"
	StatusDeadLetter Status = "dead_letter"
)

// Terminal reports whether the status forbids further processing.
func (s Status) Terminal() bool {
	switch s {
	case StatusSent, StatusFailed, StatusCanceled, StatusDeadLetter:
		return true
	}
	return false
}

// Cancelable reports whether a notification in this status can be canceled by API.
func (s Status) Cancelable() bool {
	return s == StatusPending || s == StatusProcessing
}

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusProcessing, StatusSent, StatusFailed, StatusCanceled, StatusDeadLetter:
		return true
	}
	return false
}
