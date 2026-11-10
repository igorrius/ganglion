package agent

import (
	bg "github.com/igorrius/ganglion/jobs"
)

// BackgroundJobs lists a conversation's background jobs, running and queued
// (jobs.Registry).
type BackgroundJobs interface {
	Running(conv string) []bg.Job
}

// NoBackground is the BackgroundJobs of a node that runs none.
type NoBackground struct{}

// Running implements BackgroundJobs.
func (NoBackground) Running(string) []bg.Job { return nil }

// jobsNote is the note of the conversation's background jobs, built once at
// the start of a turn (ruling 15 of the M4c plan): the model reads it with the
// turn's lines and the history never keeps it, so asked "how is it going?" it
// answers from facts (R-LIVE-9). "" when nothing runs.
func (a *Agent) jobsNote(t *turn) string {
	return bg.Note(a.d.Background.Running(t.event.Conv), t.now)
}
