package reminder

import (
	"fmt"
	"time"

	"github.com/gigurra/oh-shit-meeting/internal/calendar"
)

// AckStore defines the interface for acknowledgment storage
type AckStore interface {
	IsAcked(eventID, reminderID string) bool
	MarkAcked(eventID, reminderID string) error
}

// EventAckID marks a whole event as acknowledged — suppresses every
// reminder (custom, global, and "started") for that occurrence.
const EventAckID = "event"

// Clock provides the current time (mockable for tests)
type Clock interface {
	Now() time.Time
}

// RealClock uses the system time
type RealClock struct{}

func (c *RealClock) Now() time.Time {
	return time.Now()
}

// Info contains information about a reminder that should fire
type Info struct {
	Event       calendar.Event
	StartTime   time.Time
	EndTime     time.Time
	TimeUntil   time.Duration
	ReminderID  string
	Sound       string
	AckEventKey string // composite key: eventID + start time (handles rescheduled/recurring events)
	// Soft marks a reminder for an invite the user has not answered. Soft
	// reminders nudge instead of demanding an acknowledgement.
	Soft bool
	// AckID is the ack store ID for this reminder. Nudges live in their own
	// namespace so they never count as acknowledging the hard alert.
	AckID string
}

// nudgePrefix namespaces nudge ack IDs. Ack IDs become file names.
const nudgePrefix = "nudge-"

func ackID(reminderID string, soft bool) string {
	if soft {
		return nudgePrefix + reminderID
	}
	return reminderID
}

// UnansweredMode selects how invites without an RSVP are treated.
type UnansweredMode string

const (
	// UnansweredSoft fires a non-blocking, time-limited nudge.
	UnansweredSoft UnansweredMode = "soft"
	// UnansweredAlert treats the invite like an accepted meeting.
	UnansweredAlert UnansweredMode = "alert"
	// UnansweredIgnore never reminds about the invite.
	UnansweredIgnore UnansweredMode = "ignore"
)

// Config holds configuration for the reminder finder
type Config struct {
	WarnBefore time.Duration
	Sound      string
	// AlertUnansweredInvitations is evaluated for each reminder check so a
	// dashboard preference change takes effect immediately. Nil defaults on.
	AlertUnansweredInvitations func() bool
	Unanswered                 UnansweredMode
}

// Finder finds reminders that should fire
type Finder struct {
	ackStore AckStore
	clock    Clock
	config   Config
}

// NewFinder creates a new Finder with the given dependencies. Any
// Unanswered value other than alert or ignore means soft.
func NewFinder(ackStore AckStore, clock Clock, config Config) *Finder {
	switch config.Unanswered {
	case UnansweredAlert, UnansweredIgnore:
	default:
		config.Unanswered = UnansweredSoft
	}
	return &Finder{
		ackStore: ackStore,
		clock:    clock,
		config:   config,
	}
}

// policy is what an event's RSVP earns it: nothing, a nudge, or a full alert.
type policy int

const (
	policySilent policy = iota
	policySoft
	policyHard
)

// classify maps the user's RSVP onto reminder policy.
func (f *Finder) classify(event calendar.Event) policy {
	if event.IsDeclinedBySelf() {
		return policySilent
	}
	if event.IsAwaitingSelfResponse() {
		// The dashboard preference is the master switch. Only when it allows
		// unanswered invites through does the --unanswered mode pick soft or hard.
		if f.config.AlertUnansweredInvitations != nil && !f.config.AlertUnansweredInvitations() {
			return policySilent
		}
		switch f.config.Unanswered {
		case UnansweredIgnore:
			return policySilent
		case UnansweredAlert:
			return policyHard
		default:
			return policySoft
		}
	}
	return policyHard
}

// RecordAck records an ack for one reminder. When that completes the
// event's hard-alert set, the event-level ack is written too. Nudges never
// promote: the hard alert stays armed in case the user accepts the invite.
func (f *Finder) RecordAck(event calendar.Event, startTime time.Time, id string) error {
	ackKey := AckEventKey(event.ID, startTime)
	if err := f.ackStore.MarkAcked(ackKey, id); err != nil {
		return err
	}
	if f.classify(event) == policySoft {
		return nil
	}
	if f.IsFullyAcked(event, startTime) {
		return f.ackStore.MarkAcked(ackKey, EventAckID)
	}
	return nil
}

// AckEventKey computes a unique ack key from event ID and start time.
// This ensures rescheduled or recurring events (same ID, different time)
// get fresh ack state instead of being silenced by stale ack files.
func AckEventKey(eventID string, startTime time.Time) string {
	return eventID + "_" + startTime.UTC().Format("20060102T150405Z")
}

// ReminderState describes one configurable alert for an event occurrence
// along with its current ack state. Used by the dashboard.
type ReminderState struct {
	ID    string // ack file ID: "10m", "global", "started"
	Label string // human-readable, e.g. "10 min before"
	Acked bool
}

// Reminders enumerates every alert that can fire for this event occurrence:
// each popup override, plus global (when WarnBefore>0), plus started.
// Each is annotated with current ack state.
func (f *Finder) Reminders(event calendar.Event, startTime time.Time) []ReminderState {
	p := f.classify(event)
	if p == policySilent {
		return nil
	}
	soft := p == policySoft
	out := make([]ReminderState, 0, len(event.Reminders.Overrides)+2)
	for _, r := range event.Reminders.Overrides {
		if r.Method == "popup" {
			out = append(out, ReminderState{ID: overrideID(r.Minutes), Label: fmt.Sprintf("%d min before", r.Minutes)})
		}
	}
	if f.config.WarnBefore > 0 {
		out = append(out, ReminderState{ID: "global", Label: fmt.Sprintf("%d min before (default)", int(f.config.WarnBefore.Minutes()))})
	}
	out = append(out, ReminderState{ID: "started", Label: "when meeting starts"})

	ackKey := AckEventKey(event.ID, startTime)
	for i := range out {
		out[i].ID = ackID(out[i].ID, soft)
		if soft {
			out[i].Label += " (nudge)"
		}
		out[i].Acked = f.ackStore.IsAcked(ackKey, out[i].ID)
	}
	return out
}

func overrideID(minutes int) string {
	return fmt.Sprintf("%dm", minutes)
}

// IsFullyAcked returns true when every alert that can fire for this event
// occurrence has been acked: each custom popup override, plus the terminal
// alert (global if WarnBefore>0, else started as the only post-start alert).
// "started" counts as a fallback for global, since started only fires when
// global was missed entirely. For unanswered invites the nudge IDs are
// consulted instead. Hard-alert acks are never implied by nudges.
func (f *Finder) IsFullyAcked(event calendar.Event, startTime time.Time) bool {
	ackKey := AckEventKey(event.ID, startTime)
	soft := f.classify(event) == policySoft

	for _, r := range event.Reminders.Overrides {
		if r.Method != "popup" {
			continue
		}
		if !f.ackStore.IsAcked(ackKey, ackID(overrideID(r.Minutes), soft)) {
			return false
		}
	}

	if f.config.WarnBefore > 0 {
		return f.hasGlobalAck(ackKey, soft)
	}
	return f.ackStore.IsAcked(ackKey, ackID("started", soft))
}

// hasGlobalAck checks if the global or started reminder was acknowledged.
// Used to suppress "started" alerts when user already saw the main reminder.
// Custom reminders (10m, 30m, etc.) are early warnings and don't count.
func (f *Finder) hasGlobalAck(ackKey string, soft bool) bool {
	return f.ackStore.IsAcked(ackKey, ackID("global", soft)) ||
		f.ackStore.IsAcked(ackKey, ackID("started", soft))
}

// FindNext returns the next reminder that should fire, or nil if none.
// Events must be pre-sorted by start time.
func (f *Finder) FindNext(events []calendar.Event) *Info {
	now := f.clock.Now()

	for _, event := range events {
		p := f.classify(event)
		if p == policySilent {
			continue
		}
		soft := p == policySoft

		startTime, err := time.Parse(time.RFC3339, event.Start.DateTime)
		if err != nil {
			continue
		}
		endTime, err := time.Parse(time.RFC3339, event.End.DateTime)
		if err != nil {
			continue
		}

		timeUntil := startTime.Sub(now)
		ackKey := AckEventKey(event.ID, startTime)

		// Skip if the user ack'd the whole event from the dashboard
		if f.ackStore.IsAcked(ackKey, EventAckID) {
			continue
		}

		// Skip events that have already ended
		if now.After(endTime) {
			continue
		}

		// Check if event has already started (you're late!)
		if timeUntil < 0 {
			// Don't show "started" if user already acked the global reminder
			if f.hasGlobalAck(ackKey, soft) {
				continue
			}
			return f.newInfo(event, startTime, endTime, "started", soft)
		}

		// Check custom reminder overrides
		for _, reminder := range event.Reminders.Overrides {
			if reminder.Method != "popup" {
				continue
			}

			reminderID := overrideID(reminder.Minutes)
			reminderTime := time.Duration(reminder.Minutes) * time.Minute
			if timeUntil <= reminderTime && !f.ackStore.IsAcked(ackKey, ackID(reminderID, soft)) {
				return f.newInfo(event, startTime, endTime, reminderID, soft)
			}
		}

		// Check global warn-before threshold
		if timeUntil <= f.config.WarnBefore && !f.ackStore.IsAcked(ackKey, ackID("global", soft)) {
			return f.newInfo(event, startTime, endTime, "global", soft)
		}
	}

	return nil
}

func (f *Finder) newInfo(event calendar.Event, startTime, endTime time.Time, reminderID string, soft bool) *Info {
	return &Info{
		Event:       event,
		StartTime:   startTime,
		EndTime:     endTime,
		TimeUntil:   startTime.Sub(f.clock.Now()),
		ReminderID:  reminderID,
		Sound:       f.config.Sound,
		AckEventKey: AckEventKey(event.ID, startTime),
		Soft:        soft,
		AckID:       ackID(reminderID, soft),
	}
}
