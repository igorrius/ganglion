package cellskv

import (
	"context"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/cells"
	"github.com/nats-io/nats.go/jetstream"
)

// fetchBatch is how many stream messages one fetch of ChangesSince asks for.
const fetchBatch = 256

// kvOperation is the header that marks a delete or a purge in the stream.
const kvOperation = "KV-Operation"

// ChangesSince implements cells.Store. It reads the bucket's stream in
// order, from the message after seq, and keeps the entries that carry a
// change. The stream holds the last 64 values of each key, so a change older
// than that is gone and its Seq is skipped over.
func (s *Store) ChangesSince(ctx context.Context, seq int64, limit int) ([]cells.Change, error) {
	if limit <= 0 {
		return nil, nil
	}
	st, err := s.js.Stream(ctx, streamName)
	if err != nil {
		return nil, fmt.Errorf("cellskv: stream: %w", err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("cellskv: stream: %w", err)
	}
	seq = max(seq, 0)
	last := int64(info.State.LastSeq)
	if last <= seq {
		return nil, nil
	}
	cons, err := st.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:   uint64(seq) + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("cellskv: changes: %w", err)
	}
	return collect(ctx, cons, last, limit)
}

// collect fetches from cons, without waiting for messages that are not
// in the stream yet, until limit changes are in or the message
// numbered last was read.
func collect(ctx context.Context, cons jetstream.Consumer, last int64, limit int) ([]cells.Change, error) {
	var out []cells.Change
	for len(out) < limit {
		batch, err := cons.FetchNoWait(fetchBatch)
		if err != nil {
			return nil, fmt.Errorf("cellskv: changes: %w", err)
		}
		reached, got := false, 0
		for msg := range batch.Messages() {
			got++
			md, err := msg.Metadata()
			if err != nil {
				return nil, fmt.Errorf("cellskv: changes: %w", err)
			}
			if ch, ok := messageChange(msg, md.Sequence.Stream); ok && len(out) < limit {
				out = append(out, ch)
			}
			reached = reached || int64(md.Sequence.Stream) >= last
		}
		if err := batch.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) {
			return nil, fmt.Errorf("cellskv: changes: %w", err)
		}
		if reached || got == 0 || ctx.Err() != nil {
			break
		}
	}
	return out, ctx.Err()
}

// messageChange reads the change of one stream message; a delete marker or a
// write that made no change has none.
func messageChange(msg jetstream.Msg, seq uint64) (cells.Change, bool) {
	if msg.Headers().Get(kvOperation) != "" {
		return cells.Change{}, false
	}
	e, err := decode(msg.Data())
	if err != nil {
		return cells.Change{}, false
	}
	return e.change(seq)
}
