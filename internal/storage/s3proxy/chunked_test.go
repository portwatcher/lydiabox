package s3proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const testTrailer = "x-amz-checksum-crc32:sOO8/Q=="

// TestChunkSignatureAWSExample checks the chunk chain against the example
// in the AWS S3 documentation ("Signature Calculations for the
// Authorization Header: Transferring Payload in Multiple Chunks").
func TestChunkSignatureAWSExample(t *testing.T) {
	sc := scope{accessKey: "AKIAIOSFODNN7EXAMPLE", date: "20130524", region: "us-east-1", service: "s3"}
	c := chain{key: signingKey("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", sc), amzDate: "20130524T000000Z", scope: sc.String()}
	prev := "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9"
	for _, step := range []struct {
		size int
		want string
	}{
		{65536, "ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648"},
		{1024, "0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497"},
		{0, "b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9"},
	} {
		got := c.chunkSignature(prev, sha256Hex(bytes.Repeat([]byte("a"), step.size)))
		if got != step.want {
			t.Fatalf("chunk of %d bytes: %s, want %s", step.size, got, step.want)
		}
		prev = got
	}
}

// encodeChunks builds an aws-chunked body signed with the SDK stream signer
// seeded with seed (hex). A non-empty trailer adds the trailing header and
// its signature.
func encodeChunks(t *testing.T, chunks [][]byte, seed string, at time.Time, trailer string) []byte {
	t.Helper()
	seedRaw, err := hex.DecodeString(seed)
	if err != nil {
		t.Fatal(err)
	}
	ss := v4.NewStreamSigner(creds(testSecret), "s3", testRegion, seedRaw)
	var b bytes.Buffer
	prev := seed
	for _, c := range append(chunks, nil) {
		sig, err := ss.GetSignature(context.Background(), nil, c, at)
		if err != nil {
			t.Fatal(err)
		}
		prev = hex.EncodeToString(sig)
		fmt.Fprintf(&b, "%x;chunk-signature=%s\r\n", len(c), prev)
		if len(c) > 0 {
			b.Write(c)
			b.WriteString("\r\n")
		}
	}
	if trailer == "" {
		b.WriteString("\r\n")
		return b.Bytes()
	}
	sc := scope{accessKey: testAccess, date: at.UTC().Format("20060102"), region: testRegion, service: "s3"}
	c := chain{key: signingKey(testSecret, sc), amzDate: at.UTC().Format(amzDateFormat), scope: sc.String()}
	fmt.Fprintf(&b, "%s\r\n%s%s\r\n\r\n", trailer, trailerSigKey, c.trailerSignature(prev, []byte(trailer+"\n")))
	return b.Bytes()
}

// decodeChunks verifies an aws-chunked body with the SDK stream signer
// seeded with seed and returns the payload.
func decodeChunks(body []byte, seed string, at time.Time, trailer bool) ([]byte, error) {
	seedRaw, err := hex.DecodeString(seed)
	if err != nil {
		return nil, err
	}
	ss := v4.NewStreamSigner(creds(testSecret), "s3", testRegion, seedRaw)
	br := bufio.NewReader(bytes.NewReader(body))
	var data bytes.Buffer
	var prev string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, sig, _ := strings.Cut(strings.TrimSuffix(line, "\r\n"), ";chunk-signature=")
		n, err := strconv.ParseInt(size, 16, 64)
		if err != nil {
			return nil, err
		}
		c := make([]byte, n)
		if _, err := io.ReadFull(br, c); err != nil {
			return nil, err
		}
		want, _ := ss.GetSignature(context.Background(), nil, c, at)
		if hex.EncodeToString(want) != sig {
			return nil, fmt.Errorf("chunk signature %s, want %x", sig, want)
		}
		prev = sig
		data.Write(c)
		if n == 0 {
			break
		}
		if _, err := br.Discard(2); err != nil {
			return nil, err
		}
	}
	rest, _ := io.ReadAll(br)
	if !trailer {
		if string(rest) != "\r\n" {
			return nil, fmt.Errorf("final bytes %q", rest)
		}
		return data.Bytes(), nil
	}
	sc := scope{accessKey: testAccess, date: at.UTC().Format("20060102"), region: testRegion, service: "s3"}
	c := chain{key: signingKey(testSecret, sc), amzDate: at.UTC().Format(amzDateFormat), scope: sc.String()}
	want := testTrailer + "\r\n" + trailerSigKey + c.trailerSignature(prev, []byte(testTrailer+"\n")) + "\r\n\r\n"
	if string(rest) != want {
		return nil, fmt.Errorf("trailer %q, want %q", rest, want)
	}
	return data.Bytes(), nil
}

func TestChunkedChainRewrite(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 5000) // 80000 bytes
	chunks := [][]byte{payload[:65536], payload[65536:]}
	for _, mode := range []string{streamingSigned, streamingTrailer} {
		trailer := ""
		if mode == streamingTrailer {
			trailer = testTrailer
		}
		t.Run(mode, func(t *testing.T) {
			result := make(chan error, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want, sig, err := backendCheck(r)
				if err == nil && want != sig {
					err = fmt.Errorf("header signature %s, want %s", sig, want)
				}
				if err != nil {
					result <- err
					w.WriteHeader(http.StatusForbidden)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					result <- err
					return
				}
				at, _ := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
				data, err := decodeChunks(body, sig, at, trailer != "")
				if err == nil && !bytes.Equal(data, payload) {
					err = fmt.Errorf("payload differs (%d bytes)", len(data))
				}
				if err == nil && int64(len(body)) != r.ContentLength {
					err = fmt.Errorf("body %d bytes, Content-Length %d", len(body), r.ContentLength)
				}
				result <- err
			}))
			t.Cleanup(backend.Close)
			front := httptest.NewServer(newTestProxy(t, backend.URL, time.Now()))
			t.Cleanup(front.Close)

			send := func(tamper bool) *http.Response {
				at := time.Now()
				// The body length does not depend on the signatures, so sign
				// first with a placeholder body of the right length.
				length := len(encodeChunks(t, chunks, emptySHA256, at, trailer))
				req, _ := http.NewRequest("PUT", front.URL+"/s3/lydiabox/workspace/big.bin", nil)
				req.ContentLength = int64(length)
				req.Header.Set("Content-Encoding", "aws-chunked")
				req.Header.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(payload)))
				if trailer != "" {
					req.Header.Set("X-Amz-Trailer", "x-amz-checksum-crc32")
				}
				signHeader(t, req, mode, testSecret, at)
				a, _ := parseAuthorization(req.Header.Get("Authorization"))
				body := encodeChunks(t, chunks, a.signature, at, trailer)
				if tamper {
					body[70000] ^= 1
				}
				req.Body = io.NopCloser(bytes.NewReader(body))
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}

			resp := send(false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}

			resp = send(true)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "<Code>SignatureDoesNotMatch</Code>") {
				t.Fatalf("tampered chunk: %d %s", resp.StatusCode, b)
			}
			select {
			case <-result:
			case <-time.After(5 * time.Second):
			}
		})
	}
}
