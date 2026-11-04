package chatlab

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Languages a language step can check. Russian and Ukrainian tell apart by letters
// and common words, with no model to pay (owner, 2026-11-04: what repeats and needs
// no judgement should need no model).
const (
	LangRussian   = "ru"
	LangUkrainian = "uk"
)

const (
	onlyRussian   = "ыэъё"
	onlyUkrainian = "іїєґ"
)

var (
	wordsRussian = []string{"как", "что", "это", "вот", "или", "есть", "нет", "привет", "если", "уже", "несколько", "сегодня",
		"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	wordsUkrainian = []string{"що", "як", "це", "або", "вже", "якщо", "ось", "немає",
		"січня", "лютого", "березня", "квітня", "травня", "червня", "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"}
	quoted  = regexp.MustCompile(`«[^»]*»|"[^"]*"|“[^”]*”`)
	address = regexp.MustCompile(`\S+\.\S+/\S*|https?://\S+|\S+\.(?:net|com|org|ua)\b`)
	wordRE  = regexp.MustCompile(`[\p{L}'’]+`)
)

// CheckLanguage says whether text is written in want. A phrase in quotation
// marks, an address, a name or a currency code does not count; text with no words
// at all holds, as numbers have no language, and an empty text does not.
func CheckLanguage(want, text string) (ok bool, why string) {
	if want != LangRussian && want != LangUkrainian {
		return false, fmt.Sprintf("no check for language %q", want)
	}
	if strings.TrimSpace(text) == "" {
		return false, "the model wrote nothing"
	}
	cyrillic, latin := splitWords(address.ReplaceAllString(quoted.ReplaceAllString(text, " "), " "))
	if len(latin) > len(cyrillic) {
		return false, fmt.Sprintf("mostly Latin letters: %s", strings.Join(first(latin), " "))
	}
	letters, common, other := onlyUkrainian, wordsUkrainian, "Ukrainian"
	if want == LangUkrainian {
		letters, common, other = onlyRussian, wordsRussian, "Russian"
	}
	for _, w := range cyrillic {
		if strings.ContainsAny(w, letters) || slices.Contains(common, w) {
			return false, fmt.Sprintf("the word %q is %s", w, other)
		}
	}
	return true, ""
}

// splitWords takes the words in lower case by alphabet; a word in capitals, such
// as USD, and a name with a capital in Latin letters do not count.
func splitWords(text string) (cyrillic, latin []string) {
	for _, w := range wordRE.FindAllString(text, -1) {
		first := []rune(w)[0]
		switch {
		case unicode.Is(unicode.Cyrillic, first):
			cyrillic = append(cyrillic, strings.ToLower(w))
		case unicode.IsLower(first) && len(w) > 2:
			latin = append(latin, w)
		}
	}
	return cyrillic, latin
}

func first(words []string) []string { return words[:min(len(words), 5)] }
