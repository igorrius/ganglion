package texts

import "strings"

// native is the table of languages that can be asked for, each named in itself. It bounds what a
// person can cost the node: one translation per listed language (ADR-0053).
var native = map[Lang]string{
	"en": "English", "ru": "Русский", "uk": "Українська", "be": "Беларуская", "pl": "Polski",
	"cs": "Čeština", "sk": "Slovenčina", "sl": "Slovenščina", "hr": "Hrvatski", "sr": "Српски",
	"bg": "Български", "de": "Deutsch", "nl": "Nederlands", "sv": "Svenska", "nb": "Norsk bokmål",
	"da": "Dansk", "fi": "Suomi", "et": "Eesti", "lv": "Latviešu", "lt": "Lietuvių",
	"fr": "Français", "es": "Español", "pt": "Português", "it": "Italiano", "ro": "Română",
	"hu": "Magyar", "el": "Ελληνικά", "tr": "Türkçe", "az": "Azərbaycanca", "kk": "Қазақша",
	"uz": "Oʻzbekcha", "ka": "ქართული", "hy": "Հայերեն", "ar": "العربية", "he": "עברית",
	"fa": "فارسی", "hi": "हिन्दी", "bn": "বাংলা", "id": "Bahasa Indonesia", "ms": "Bahasa Melayu",
	"vi": "Tiếng Việt", "th": "ไทย", "zh": "中文", "ja": "日本語", "ko": "한국어",
}

// maxCode is the longest language tag worth reading; RFC 5646 keeps real ones far below it.
const maxCode = 35

// Normalize returns the language of an IETF tag: its primary subtag, lowercased. It is false for a
// tag whose language is not in the table, so a channel's value never reaches the store unchecked.
func Normalize(code string) (Lang, bool) {
	if code == "" || len(code) > maxCode {
		return "", false
	}
	primary, _, _ := strings.Cut(strings.ReplaceAll(code, "_", "-"), "-")
	l := Lang(strings.ToLower(primary))
	if _, ok := native[l]; !ok {
		return "", false
	}
	return l, true
}

// Native returns l's name in l itself, or "" for a language not in the table.
func Native(l Lang) string { return native[l] }
