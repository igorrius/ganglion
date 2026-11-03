package web

import (
	"mime"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
)

// decode returns body as UTF-8. The charset comes from the Content-Type
// header; for HTML also from a byte-order mark or a <meta> in the first
// KiB. A guess never beats a body that is valid UTF-8 (Ukrainian sites
// still serve windows-1251, often declared only in a <meta>). Bytes that
// do not decode become U+FFFD.
func decode(body []byte, header string, html bool) string {
	enc := declared(body, header, html)
	if enc == nil {
		return strings.ToValidUTF8(string(body), "�")
	}
	out, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return strings.ToValidUTF8(string(body), "�")
	}
	return strings.ToValidUTF8(string(out), "�")
}

// declared is the encoding the response states, or nil for UTF-8.
func declared(body []byte, header string, html bool) encoding.Encoding {
	if html {
		enc, name, certain := charset.DetermineEncoding(body, header)
		if name == "utf-8" || !certain && utf8.Valid(body) {
			return nil
		}
		return enc
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil || params["charset"] == "" {
		return nil
	}
	enc, name := charset.Lookup(params["charset"])
	if name == "utf-8" {
		return nil
	}
	return enc
}
