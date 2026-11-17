package backup

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"math"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

func sealBytes(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openAll(sealed, key []byte) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(sealed), key)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize} {
		plain := randomBytes(t, n)
		got, err := openAll(sealBytes(t, testKey(1), plain), testKey(1))
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d: got %d bytes, err %v", n, len(got), err)
		}
	}
}

func TestSealWritesInSmallPieces(t *testing.T) {
	plain := randomBytes(t, 2*chunkSize+5)
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, testKey(1))
	for p := plain; len(p) > 0; {
		n := min(len(p), 1000)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	_ = w.Close()
	got, err := openAll(buf.Bytes(), testKey(1))
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("got %d bytes, err %v", len(got), err)
	}
}

func TestSealSizeOnTheWire(t *testing.T) {
	// A stream of exactly one chunk is one sealed chunk: no empty trailer.
	if got, want := len(sealBytes(t, testKey(1), make([]byte, chunkSize))), headerSize+chunkSize+tagSize; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if got, want := len(sealBytes(t, testKey(1), nil)), headerSize+tagSize; got != want {
		t.Fatalf("empty: len = %d, want %d", got, want)
	}
}

func TestSealEachStreamHasItsOwnSalt(t *testing.T) {
	a, b := sealBytes(t, testKey(1), []byte("x")), sealBytes(t, testKey(1), []byte("x"))
	if bytes.Equal(a[:headerSize], b[:headerSize]) {
		t.Fatal("two streams share a header")
	}
}

func TestSealRefusesWhatIsNotOurs(t *testing.T) {
	good := sealBytes(t, testKey(1), []byte("hello"))
	badMagic := bytes.Clone(good)
	badMagic[0] ^= 1
	badVersion := bytes.Clone(good)
	badVersion[len(magic)] = 2
	for name, in := range map[string][]byte{"magic": badMagic, "version": badVersion, "short": good[:10], "empty": nil} {
		if _, err := NewReader(bytes.NewReader(in), testKey(1)); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
	}
	if _, err := NewReader(bytes.NewReader(good), []byte("short")); !errors.Is(err, ErrKey) {
		t.Errorf("short key: err = %v", err)
	}
	if _, err := NewWriter(io.Discard, nil); !errors.Is(err, ErrKey) {
		t.Errorf("writer, no key: err = %v", err)
	}
}

func TestSealRefusesTheWrongKey(t *testing.T) {
	_, err := openAll(sealBytes(t, testKey(1), []byte("hello")), testKey(2))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestSealRefusesAnyChangedByte(t *testing.T) {
	sealed := sealBytes(t, testKey(1), randomBytes(t, chunkSize+100))
	// The header is not authenticated by being read but by every chunk.
	for _, i := range []int{len(magic) + 1, len(magic) + 1 + saltSize, headerSize, headerSize + 5, headerSize + chunkSize + tagSize - 1, len(sealed) - 1} {
		bad := bytes.Clone(sealed)
		bad[i] ^= 0x80
		if _, err := openAll(bad, testKey(1)); !errors.Is(err, ErrCorrupt) {
			t.Errorf("byte %d: err = %v, want ErrCorrupt", i, err)
		}
	}
}

func TestSealRefusesReorderedChunks(t *testing.T) {
	sealed := sealBytes(t, testKey(1), randomBytes(t, 3*chunkSize+7))
	const full = chunkSize + tagSize
	c0 := sealed[headerSize : headerSize+full]
	c1 := sealed[headerSize+full : headerSize+2*full]
	swapped := bytes.Clone(sealed)
	copy(swapped[headerSize:], c1)
	copy(swapped[headerSize+full:], c0)
	if _, err := openAll(swapped, testKey(1)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestSealRefusesACutStream(t *testing.T) {
	sealed := sealBytes(t, testKey(1), randomBytes(t, 3*chunkSize+7))
	const full = chunkSize + tagSize
	for name, n := range map[string]int{
		"at a chunk boundary": headerSize + 2*full,
		"in a chunk":          headerSize + full + 100,
		"header only":         headerSize,
		"last chunk missing":  len(sealed) - (7 + tagSize),
	} {
		if _, err := openAll(sealed[:n], testKey(1)); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
}

func TestSealRefusesDataAfterTheLastChunk(t *testing.T) {
	for _, n := range []int{0, 5, chunkSize} {
		sealed := sealBytes(t, testKey(1), randomBytes(t, n))
		// Another stream's chunk, a whole chunk of noise, and one byte.
		other := sealBytes(t, testKey(1), []byte("more"))[headerSize:]
		for name, extra := range map[string][]byte{"chunk": other, "noise": randomBytes(t, chunkSize+tagSize), "byte": {0}} {
			if _, err := openAll(append(bytes.Clone(sealed), extra...), testKey(1)); !errors.Is(err, ErrCorrupt) {
				t.Errorf("size %d, %s: err = %v, want ErrCorrupt", n, name, err)
			}
		}
	}
}

func TestSealReleasesNothingBeforeItsTagVerifies(t *testing.T) {
	sealed := sealBytes(t, testKey(1), randomBytes(t, 2*chunkSize))
	sealed[headerSize+3] ^= 1 // the first chunk
	r, err := NewReader(bytes.NewReader(sealed), testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.Read(make([]byte, 100))
	if n != 0 || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("read = %d, %v; want 0, ErrCorrupt", n, err)
	}
	// The error is final.
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("second read: %v", err)
	}
}

func TestSealCounterOverflow(t *testing.T) {
	aead, err := newAEAD(testKey(1), make([]byte, saltSize))
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, headerSize)
	copy(header, magic)
	header[len(magic)] = version1
	// A writer at the last counter value cannot seal a chunk that is not last.
	w := &sealWriter{w: io.Discard, aead: aead, header: header, buf: make([]byte, chunkSize), counter: math.MaxUint32}
	if err := w.flush(false); !errors.Is(err, ErrTooLong) {
		t.Fatalf("writer: %v", err)
	}
	// A reader at the last counter value refuses a chunk that is not last.
	prefix := header[len(magic)+1+saltSize:]
	ct := aead.Seal(nil, chunkNonce(prefix, math.MaxUint32, false), make([]byte, chunkSize), header)
	stream := append(bytes.Clone(ct), 0)
	r := &sealReader{r: bufioReader(stream), aead: aead, header: header, ct: make([]byte, chunkSize+tagSize),
		plain: make([]byte, 0, chunkSize), counter: math.MaxUint32}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("reader: %v", err)
	}
}

func bufioReader(b []byte) *bufio.Reader {
	return bufio.NewReaderSize(bytes.NewReader(b), chunkSize+tagSize+1)
}
