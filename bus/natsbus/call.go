package natsbus

import (
	"context"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// Serve registers a NATS micro service named service, at version (a semantic
// version), with one endpoint per entry of endpoints, each on its own
// subject with its metadata, and answers each request with the endpoint's
// handler. Discovery and per-endpoint statistics come with the service. The
// returned stop drains the endpoints and returns at once: a request in hand
// is still answered. A service that cannot be made is stopped and its
// error returned (R-BUS-6).
func (b *Bus) Serve(service, version string, endpoints []bus.Endpoint) (stop func() error, err error) {
	if b.nc == nil {
		return nil, bus.ErrNotConnected
	}
	svc, err := micro.AddService(b.nc, micro.Config{Name: service, Version: version})
	if err != nil {
		return nil, fmt.Errorf("natsbus: serve %s: %w", service, err)
	}
	for _, e := range endpoints {
		h := micro.HandlerFunc(func(req micro.Request) { answer(e.Handler, req) })
		if err := svc.AddEndpoint(e.Name, h, micro.WithEndpointSubject(e.Subject), micro.WithEndpointMetadata(e.Metadata)); err != nil {
			_ = svc.Stop()
			return nil, fmt.Errorf("natsbus: serve %s: endpoint %s: %w", service, e.Name, err)
		}
	}
	return svc.Stop, nil
}

// answer runs the handler for one request and sends its answer or its
// error. A failure to send is the caller's timeout: nothing more to do.
func answer(h bus.EndpointHandler, req micro.Request) {
	headers := make(map[string]string, len(req.Headers()))
	for k := range req.Headers() {
		headers[k] = req.Headers().Get(k)
	}
	body, err := h(context.Background(), bus.Request{Headers: headers, Payload: req.Data()})
	var ce *bus.CallError
	switch {
	case errors.As(err, &ce):
		_ = req.Error(ce.Code, ce.Description, nil)
	case err != nil:
		_ = req.Error(bus.CodeFailed, "the call failed", nil)
	default:
		_ = req.Respond(body)
	}
}

// Request sends payload to subject with headers and waits for the answer,
// until ctx ends. Nobody serving the subject is bus.ErrNoResponder at once;
// an error answer is a *bus.CallError; a ctx that ends first is its own
// error, wrapped.
func (b *Bus) Request(ctx context.Context, subject string, headers map[string]string, payload []byte) ([]byte, error) {
	if b.nc == nil {
		return nil, bus.ErrNotConnected
	}
	msg := nats.NewMsg(subject)
	msg.Data = payload
	for k, v := range headers {
		msg.Header.Set(k, v)
	}
	resp, err := b.nc.RequestMsgWithContext(ctx, msg)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return nil, bus.ErrNoResponder
	case err != nil:
		return nil, fmt.Errorf("natsbus: request %s: %w", subject, err)
	}
	if code := resp.Header.Get(micro.ErrorCodeHeader); code != "" {
		return nil, &bus.CallError{Code: code, Description: resp.Header.Get(micro.ErrorHeader)}
	}
	return resp.Data, nil
}
