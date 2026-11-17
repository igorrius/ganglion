// Package backup makes the node's daily backup and restores it
// (R-BAK-1 – R-BAK-6, ADR-0095): a consistent copy of the database, the
// workspace, the modules and the settings in one tar.gz archive with a
// manifest, sealed with a key derived from the master key, put in an
// S3-compatible bucket with one signed request, and opened again on the
// host by Restore.
package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// The sealed stream, version 1 (R-BAK-2):
//
//	header  = magic (7) | version (1) | salt (32) | nonce prefix (7)
//	chunk   = AES-256-GCM(plaintext of 64 KiB, the last one 0..64 KiB) | tag (16)
//
// The key is HKDF-SHA256 of the master key with the salt and kdfInfo. A
// chunk's nonce is the prefix, the chunk's counter (uint32, big endian) and
// a flag byte that is 1 for the last chunk. The whole header is the
// additional data of every chunk, so a changed header opens nothing.
const (
	magic      = "GNGLBAK"
	version1   = 1
	saltSize   = 32
	prefixSize = 7
	headerSize = len(magic) + 1 + saltSize + prefixSize
	chunkSize  = 64 << 10
	kdfInfo    = "ganglion-backup-v1"
	// KeySize is the length of the master key a stream is sealed under.
	KeySize   = 32
	tagSize   = 16
	nonceSize = prefixSize + 4 + 1
)

var (
	// ErrFormat means the stream is not a sealed backup this build reads:
	// the magic or the version is not ours.
	ErrFormat = errors.New("backup: not a sealed backup of a known version")
	// ErrCorrupt means the stream does not open: a wrong key, a changed,
	// reordered, cut or extended stream. The cases are not told apart on
	// purpose: nothing is released before its chunk is authenticated.
	ErrCorrupt = errors.New("backup: sealed stream is damaged or the key is wrong")
	// ErrKey means a master key that is not KeySize bytes long.
	ErrKey = errors.New("backup: the key must be 32 bytes")
	// ErrTooLong means a stream of more chunks than a counter holds.
	ErrTooLong = errors.New("backup: sealed stream is too long")
)

// newAEAD derives the stream's key from the master key and the salt.
func newAEAD(master, salt []byte) (cipher.AEAD, error) {
	if len(master) != KeySize {
		return nil, ErrKey
	}
	key, err := hkdf.Key(sha256.New, master, salt, kdfInfo, KeySize)
	if err != nil {
		return nil, fmt.Errorf("backup: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backup: cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// chunkNonce is the nonce of chunk counter under prefix.
func chunkNonce(prefix []byte, counter uint32, last bool) []byte {
	n := make([]byte, nonceSize)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[prefixSize:], counter)
	if last {
		n[nonceSize-1] = 1
	}
	return n
}

type sealWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	header  []byte
	buf     []byte
	out     []byte
	n       int
	counter uint32
	err     error
	closed  bool
}

// NewWriter starts a sealed stream on w under the master key and writes its
// header. Close seals the last chunk (an empty stream still has one); it does
// not close w. A stream that is not closed does not open.
func NewWriter(w io.Writer, key []byte) (io.WriteCloser, error) {
	header := make([]byte, headerSize)
	copy(header, magic)
	header[len(magic)] = version1
	if _, err := rand.Read(header[len(magic)+1:]); err != nil {
		return nil, fmt.Errorf("backup: random: %w", err)
	}
	aead, err := newAEAD(key, header[len(magic)+1:len(magic)+1+saltSize])
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, fmt.Errorf("backup: write header: %w", err)
	}
	return &sealWriter{w: w, aead: aead, header: header,
		buf: make([]byte, chunkSize), out: make([]byte, 0, chunkSize+tagSize)}, nil
}

func (s *sealWriter) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("backup: write to a closed sealed stream")
	}
	total := 0
	for len(p) > 0 {
		// A full buffer is sealed only when more data follows, so the
		// last chunk is never empty unless the whole stream is.
		if s.n == chunkSize {
			if err := s.flush(false); err != nil {
				return total, err
			}
		}
		c := copy(s.buf[s.n:], p)
		s.n += c
		p = p[c:]
		total += c
	}
	return total, nil
}

// flush seals the buffered plaintext as the next chunk.
func (s *sealWriter) flush(last bool) error {
	if s.err != nil {
		return s.err
	}
	if !last && s.counter == math.MaxUint32 {
		s.err = ErrTooLong
		return s.err
	}
	nonce := chunkNonce(s.header[len(magic)+1+saltSize:], s.counter, last)
	s.out = s.aead.Seal(s.out[:0], nonce, s.buf[:s.n], s.header)
	if _, err := s.w.Write(s.out); err != nil {
		s.err = fmt.Errorf("backup: write chunk: %w", err)
		return s.err
	}
	s.counter++
	s.n = 0
	return nil
}

func (s *sealWriter) Close() error {
	if s.closed {
		return s.err
	}
	s.closed = true
	return s.flush(true)
}

type sealReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	header  []byte
	ct      []byte
	pending []byte
	plain   []byte
	counter uint32
	done    bool
	err     error
}

// NewReader opens a sealed stream under the master key. It reads and checks
// the header at once and returns ErrFormat for a stream that is not ours.
// Read releases a chunk's plaintext only after the chunk's tag verifies; a
// damaged, reordered, cut or extended stream ends in ErrCorrupt.
func NewReader(r io.Reader, key []byte) (io.Reader, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: short header", ErrFormat)
		}
		return nil, fmt.Errorf("backup: read header: %w", err)
	}
	if !bytes.Equal(header[:len(magic)], []byte(magic)) || header[len(magic)] != version1 {
		return nil, ErrFormat
	}
	aead, err := newAEAD(key, header[len(magic)+1:len(magic)+1+saltSize])
	if err != nil {
		return nil, err
	}
	return &sealReader{r: bufio.NewReaderSize(r, chunkSize+tagSize+1), aead: aead, header: header,
		ct: make([]byte, chunkSize+tagSize), plain: make([]byte, 0, chunkSize)}, nil
}

func (s *sealReader) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		if s.done {
			return 0, io.EOF
		}
		if err := s.fill(); err != nil {
			s.err = err
			return 0, err
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// fill reads, authenticates and buffers the next chunk. Every chunk but the
// last is full, so a full chunk is the last one exactly when nothing follows.
func (s *sealReader) fill() error {
	n, err := io.ReadFull(s.r, s.ct)
	last := false
	switch {
	case err == nil:
		_, perr := s.r.Peek(1)
		switch {
		case errors.Is(perr, io.EOF):
			last = true
		case perr != nil:
			return fmt.Errorf("backup: read chunk: %w", perr)
		}
	case errors.Is(err, io.ErrUnexpectedEOF):
		last = true
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: the stream ends before its last chunk", ErrCorrupt)
	default:
		return fmt.Errorf("backup: read chunk: %w", err)
	}
	if n < tagSize || (!last && s.counter == math.MaxUint32) {
		return ErrCorrupt
	}
	nonce := chunkNonce(s.header[len(magic)+1+saltSize:], s.counter, last)
	plain, err := s.aead.Open(s.plain[:0], nonce, s.ct[:n], s.header)
	if err != nil {
		return ErrCorrupt
	}
	s.plain, s.pending = plain, plain
	s.counter++
	s.done = last
	return nil
}
