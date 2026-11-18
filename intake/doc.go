// Package intake takes values handed over from the chat: a one-time OAuth
// code and a secret sealed in the admin's browser (spec 7.17,
// [ADR-0098]).
//
// The node opens a request (Registry.OpenOAuth, Registry.OpenSecret); the
// admin's browser shows a line, and the admin pastes it into the chat. The
// transport recognises such a message with IsLine, takes it before the
// inbox and hands it to Registry.Take. A request is opened by the node
// only, never by the model (R-INT-1): no tool exposes the registry.
//
// A line, a code and an opened value never reach a log line, an error
// text, a reply or the transport's sink. What the package logs and counts
// is the request id and an outcome from a closed set.
//
// [ADR-0098]: https://github.com/igorrius/ganglion/blob/master/docs/adr/0098-hand-values-over-from-the-chat-through-a-pinned-static-page.md
package intake
