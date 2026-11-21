package natsbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

// kvOperation is the header that marks a delete or a purge in a bucket's
// stream; a message without it is a put.
const kvOperation = "KV-Operation"

// exportBatch is how many stream messages one fetch of ExportKV asks for.
const exportBatch = 256

// Operation names of an export line.
const (
	opPut    = "put"
	opDelete = "delete"
	opPurge  = "purge"
)

// ErrNotEmpty means a bucket that is to be imported into already holds
// entries.
var ErrNotEmpty = errors.New("natsbus: the bucket is not empty")

// ErrBadExport means an export that cannot be read back: a line that is not
// JSON, an operation that is not known, or lines out of order.
var ErrBadExport = errors.New("natsbus: the export is not valid")

// kvLine is one line of an export: one message of the bucket's stream, in
// the stream's order. Value is the entry's bytes as they were stored
// (base64 in JSON), so history is kept as it was.
type kvLine struct {
	Key   string `json:"key"`
	Seq   uint64 `json:"seq"`
	Op    string `json:"op"`
	Value []byte `json:"value"`
}

// kvStream is the name of the stream behind a bucket, and the prefix of the
// subjects of its keys.
func kvStream(bucket string) string { return "KV_" + bucket }
func kvPrefix(bucket string) string { return "$KV." + bucket + "." }

// ExportKV writes every message of the bucket's stream to w in sequence
// order, deletes and purges included, one JSON object per line:
// {"key","seq","op","value"}. The values are the entries' bytes verbatim, so
// a bucket's history can be loaded again as it was (R-BUS-4, R-BAK-1). It
// stops at the last message the stream held when it began, and returns how
// many lines it wrote.
func (b *Bus) ExportKV(ctx context.Context, bucket string, w io.Writer) (int, error) {
	st, err := b.js.Stream(ctx, kvStream(bucket))
	if err != nil {
		return 0, fmt.Errorf("natsbus: export %s: %w", bucket, err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("natsbus: export %s: %w", bucket, err)
	}
	if info.State.Msgs == 0 {
		return 0, nil
	}
	cons, err := st.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy})
	if err != nil {
		return 0, fmt.Errorf("natsbus: export %s: %w", bucket, err)
	}
	enc := json.NewEncoder(w)
	n, err := exportMessages(ctx, cons, kvPrefix(bucket), info.State.LastSeq, enc)
	if err != nil {
		return n, fmt.Errorf("natsbus: export %s: %w", bucket, err)
	}
	return n, nil
}

// exportMessages reads cons until the message numbered last and writes each
// as a line.
func exportMessages(ctx context.Context, cons jetstream.Consumer, prefix string, last uint64, enc *json.Encoder) (int, error) {
	n := 0
	for {
		got, reached, err := exportBatchOf(cons, prefix, last, enc)
		n += got
		if err != nil || reached {
			return n, err
		}
		if got == 0 {
			// The stream held `last` when the export began, so nothing
			// ending short of it is a complete copy.
			if err := ctx.Err(); err != nil {
				return n, err
			}
			return n, fmt.Errorf("the stream ended before its message %d", last)
		}
	}
}

// exportBatchOf fetches the messages that are there now and writes them as
// lines. It reports how many it wrote and whether the message numbered last
// was among them.
func exportBatchOf(cons jetstream.Consumer, prefix string, last uint64, enc *json.Encoder) (got int, reached bool, err error) {
	batch, err := cons.FetchNoWait(exportBatch)
	if err != nil {
		return 0, false, err
	}
	for msg := range batch.Messages() {
		md, err := msg.Metadata()
		if err != nil {
			return got, reached, err
		}
		if err := enc.Encode(lineOf(msg, prefix, md.Sequence.Stream)); err != nil {
			return got, reached, err
		}
		got++
		reached = reached || md.Sequence.Stream >= last
	}
	if err := batch.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) {
		return got, reached, err
	}
	return got, reached, nil
}

// lineOf is the export line of one stream message.
func lineOf(msg jetstream.Msg, prefix string, seq uint64) kvLine {
	op := opPut
	switch msg.Headers().Get(kvOperation) {
	case "DEL":
		op = opDelete
	case "PURGE":
		op = opPurge
	}
	return kvLine{Key: strings.TrimPrefix(msg.Subject(), prefix), Seq: seq, Op: op, Value: msg.Data()}
}

// ImportKV replays an export made by ExportKV into the bucket, which must be
// empty: a bucket that holds entries is ErrNotEmpty, naming how many, and
// nothing is written. The lines are applied in order with Put, Delete and
// Purge, with the values as they are; the revisions are new, the content and
// its order are the same. When a line fails, what was written is removed
// again, so the bucket is empty as it was. It returns how many lines it
// applied.
func (b *Bus) ImportKV(ctx context.Context, bucket string, r io.Reader) (int, error) {
	st, err := b.js.Stream(ctx, kvStream(bucket))
	if err != nil {
		return 0, fmt.Errorf("natsbus: import into %s: %w", bucket, err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("natsbus: import into %s: %w", bucket, err)
	}
	if info.State.Msgs != 0 {
		return 0, fmt.Errorf("%w: %s holds %d entries", ErrNotEmpty, bucket, info.State.Msgs)
	}
	kv, err := b.js.KeyValue(ctx, bucket)
	if err != nil {
		return 0, fmt.Errorf("natsbus: import into %s: %w", bucket, err)
	}
	n, err := replay(ctx, kv, r)
	if err != nil {
		if perr := st.Purge(ctx); perr != nil {
			err = errors.Join(err, fmt.Errorf("natsbus: undo the import into %s: %w", bucket, perr))
		}
		return 0, fmt.Errorf("natsbus: import into %s: %w", bucket, err)
	}
	return n, nil
}

// replay applies the lines of r to kv in order.
func replay(ctx context.Context, kv jetstream.KeyValue, r io.Reader) (int, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	n := 0
	var prev uint64
	for {
		var l kvLine
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			return n, nil
		} else if err != nil {
			return n, fmt.Errorf("%w: line %d: %w", ErrBadExport, n+1, err)
		}
		if l.Seq <= prev {
			return n, fmt.Errorf("%w: line %d is out of order", ErrBadExport, n+1)
		}
		prev = l.Seq
		if err := apply(ctx, kv, l); err != nil {
			return n, fmt.Errorf("line %d: %w", n+1, err)
		}
		n++
	}
}

// apply does one line's operation.
func apply(ctx context.Context, kv jetstream.KeyValue, l kvLine) error {
	var err error
	switch l.Op {
	case opPut:
		_, err = kv.Put(ctx, l.Key, bytes.Clone(l.Value))
	case opDelete:
		err = kv.Delete(ctx, l.Key)
	case opPurge:
		err = kv.Purge(ctx, l.Key)
	default:
		return fmt.Errorf("%w: operation %q is not known", ErrBadExport, l.Op)
	}
	return err
}
