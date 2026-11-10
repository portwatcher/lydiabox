package s3proxy

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Limits of the aws-chunked encoding (the same as the MinIO server).
const (
	maxChunkSize  = 16 << 20
	maxChunkLine  = 4096
	trailerSigKey = "x-amz-trailer-signature:"
)

// chain is the chunk signature chain of a signed streaming upload: the
// client chain starts at the client's request signature, the backend chain
// at the re-signed one. Both use the same signing key, date, and scope.
type chain struct {
	key         []byte
	amzDate     string
	scope       string
	clientSeed  string
	backendSeed string
	trailer     bool
}

// chunkSignature returns the signature of one chunk after prev.
func (c chain) chunkSignature(prev string, dataHash string) string {
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + c.amzDate + "\n" + c.scope + "\n" + prev + "\n" + emptySHA256 + "\n" + dataHash
	return hex.EncodeToString(hmacSHA256(c.key, []byte(sts)))
}

// trailerSignature returns the signature of the trailing headers after prev.
func (c chain) trailerSignature(prev string, trailers []byte) string {
	sts := "AWS4-HMAC-SHA256-TRAILER\n" + c.amzDate + "\n" + c.scope + "\n" + prev + "\n" + sha256Hex(trailers)
	return hex.EncodeToString(hmacSHA256(c.key, []byte(sts)))
}

var errMalformedChunk = errInvalidRequest("malformed aws-chunked body")

// chunkRewriter reads an aws-chunked body signed with the client chain,
// verifies each chunk signature, and emits the same bytes with the backend
// chain. Signatures have a fixed length, so the body length does not
// change. A verification failure ends the stream with an *s3Error.
type chunkRewriter struct {
	src          *bufio.Reader
	body         io.ReadCloser
	c            chain
	client, back string // previous signatures
	out          bytes.Buffer
	finished     bool
	err          error
}

func newChunkRewriter(body io.ReadCloser, c chain) *chunkRewriter {
	return &chunkRewriter{src: bufio.NewReaderSize(body, 64<<10), body: body, c: c, client: c.clientSeed, back: c.backendSeed}
}

// Read implements io.Reader.
func (cr *chunkRewriter) Read(p []byte) (int, error) {
	for cr.out.Len() == 0 {
		if cr.err != nil {
			return 0, cr.err
		}
		if cr.finished {
			return 0, io.EOF
		}
		cr.err = cr.next()
	}
	return cr.out.Read(p)
}

// Close implements io.Closer.
func (cr *chunkRewriter) Close() error { return cr.body.Close() }

// next processes one chunk, or the final chunk with its trailer.
func (cr *chunkRewriter) next() error {
	line, err := cr.line()
	if err != nil {
		return err
	}
	sizeHex, sig, ok := strings.Cut(line, ";chunk-signature=")
	if !ok || !hexSignature.MatchString(sig) {
		return errMalformedChunk
	}
	size, err := strconv.ParseInt(sizeHex, 16, 64)
	if err != nil || size < 0 {
		return errMalformedChunk
	}
	if size > maxChunkSize {
		return errInvalidRequest("aws-chunked chunks are limited to %d bytes", maxChunkSize)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(cr.src, data); err != nil {
		return errMalformedChunk
	}
	hash := sha256Hex(data)
	if !signatureEqual(cr.c.chunkSignature(cr.client, hash), sig) {
		return errSignature()
	}
	cr.client = strings.ToLower(sig)
	cr.back = cr.c.chunkSignature(cr.back, hash)
	cr.out.WriteString(sizeHex + ";chunk-signature=" + cr.back + "\r\n")
	cr.out.Write(data)
	if size > 0 {
		if err := cr.crlf(); err != nil {
			return errMalformedChunk
		}
		cr.out.WriteString("\r\n")
		return nil
	}
	cr.finished = true
	if cr.c.trailer {
		return cr.trailers()
	}
	// The final chunk ends with an empty line; tolerate a body that stops
	// right after the chunk header.
	if err := cr.crlf(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	cr.out.WriteString("\r\n")
	return nil
}

// trailers verifies and re-signs the trailing headers that follow the final
// chunk: "name:value" lines, then "x-amz-trailer-signature:<sig>", then an
// empty line.
func (cr *chunkRewriter) trailers() error {
	var canon bytes.Buffer
	var raw []string
	for {
		line, err := cr.line()
		if err != nil {
			return err
		}
		if sig, ok := strings.CutPrefix(line, trailerSigKey); ok {
			if !hexSignature.MatchString(sig) {
				return errMalformedChunk
			}
			if !signatureEqual(cr.c.trailerSignature(cr.client, canon.Bytes()), sig) {
				return errSignature()
			}
			cr.back = cr.c.trailerSignature(cr.back, canon.Bytes())
			break
		}
		if line == "" || len(raw) >= 16 {
			return errMalformedChunk
		}
		raw = append(raw, line)
		canon.WriteString(line + "\n")
	}
	for _, l := range raw {
		cr.out.WriteString(l + "\r\n")
	}
	cr.out.WriteString(trailerSigKey + cr.back + "\r\n")
	if err := cr.crlf(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	cr.out.WriteString("\r\n")
	return nil
}

// line reads one CRLF-terminated line without the CRLF.
func (cr *chunkRewriter) line() (string, error) {
	var b []byte
	for {
		frag, err := cr.src.ReadSlice('\n')
		b = append(b, frag...)
		if len(b) > maxChunkLine {
			return "", errMalformedChunk
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return "", errMalformedChunk
	}
	s, ok := strings.CutSuffix(string(b), "\r\n")
	if !ok {
		return "", errMalformedChunk
	}
	return s, nil
}

// crlf consumes "\r\n". It returns io.EOF when the body ends first.
func (cr *chunkRewriter) crlf() error {
	var b [2]byte
	n, err := io.ReadFull(cr.src, b[:])
	if n == 0 && errors.Is(err, io.EOF) {
		return io.EOF
	}
	if err != nil || b != [2]byte{'\r', '\n'} {
		return errMalformedChunk
	}
	return nil
}
