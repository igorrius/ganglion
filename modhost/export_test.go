package modhost

// WriterWaiting reports whether a reload waits for the host's lock: a
// reader that came now would queue behind it. A test uses it to know the
// reload is in line before it lets the call go on.
func (h *Host) WriterWaiting() bool {
	if h.mu.TryRLock() {
		h.mu.RUnlock()
		return false
	}
	return true
}

// WebHeld is how many requests of the page module short are admitted now.
func (h *Host) WebHeld(short string) int {
	if l := h.pageOf(short); l != nil {
		return len(l.web)
	}
	return -1
}
