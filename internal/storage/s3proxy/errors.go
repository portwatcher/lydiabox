package s3proxy

import (
	"encoding/xml"
	"fmt"
	"net/http"
)

// s3Error is an S3 error response.
type s3Error struct {
	status  int
	code    string
	message string
}

func (e *s3Error) Error() string { return e.code + ": " + e.message }

func errInvalidRequest(format string, a ...any) *s3Error {
	return &s3Error{http.StatusBadRequest, "InvalidRequest", fmt.Sprintf(format, a...)}
}

func errAccessDenied(format string, a ...any) *s3Error {
	return &s3Error{http.StatusForbidden, "AccessDenied", fmt.Sprintf(format, a...)}
}

func errSignature() *s3Error {
	return &s3Error{http.StatusForbidden, "SignatureDoesNotMatch",
		"The request signature we calculated does not match the signature you provided. Check your key and signing method."}
}

func errInvalidAccessKey() *s3Error {
	return &s3Error{http.StatusForbidden, "InvalidAccessKeyId", "The AWS access key ID you provided does not exist in our records."}
}

func errSkewed() *s3Error {
	return &s3Error{http.StatusForbidden, "RequestTimeTooSkewed",
		"The difference between the request time and the server's time is too large."}
}

type xmlError struct {
	XMLName  xml.Name `xml:"Error"`
	Code     string   `xml:"Code"`
	Message  string   `xml:"Message"`
	Resource string   `xml:"Resource"`
}

// write sends e as an S3 XML error document.
func (e *s3Error) write(w http.ResponseWriter, r *http.Request) {
	body, err := xml.Marshal(xmlError{Code: e.code, Message: e.message, Resource: r.URL.Path})
	if err != nil {
		http.Error(w, e.Error(), e.status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(xml.Header))
		_, _ = w.Write(body)
	}
}
