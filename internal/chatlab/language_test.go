package chatlab

import "testing"

func TestCheckLanguage(t *testing.T) {
	for _, tc := range []struct {
		name, want, text string
		ok               bool
	}{
		{"russian", "ru", "Привет, Игорь! Чем могу помочь?", true},
		{"ukrainian", "uk", "Привіт! Ось кілька простих варіантів для вечері.", true},
		{"english for russian", "ru", "Exchange rates for 4 November 2026: USD 45.666 UAH.", false},
		{"english for ukrainian", "uk", "Igor, the first tip is to keep the battery above 20% at night.", false},
		{"ukrainian for russian", "ru", "Курс на сьогодні (4 листопада): USD 45,666 ₴", false},
		{"russian for ukrainian", "uk", "Вот несколько простых вариантов на ужин.", false},
		{"a word slipped in", "ru", "Курс на сьогодні (4 листопада) по официальным данным страницы: USD 45,666 грн", false},
		{"month alone gives it away", "ru", "Курс на 4 листопада 2026: USD 45,666 ₴", false},
		{"short russian for ukrainian", "uk", "Привет, как дела?", false},
		{"numbers only", "ru", "USD: 45,666 ₴ EUR: 51,0582 ₴", true},
		{"nothing", "ru", "", false},
		{"a quoted phrase", "ru", "На странице указано только «станом на сьогодні» (на сегодня), без даты.", true},
		{"names and codes", "ru", "Igor, Acme 8 kW и Wi-Fi логгер: обновите прошивку до зимы.", true},
		{"an address", "uk", "Джерело: rates.example.net/today, офіційний курс НБУ.", true},
		{"an apostrophe", "uk", "М'ясо запікайте при 200 °C близько години.", true},
		{"unknown language", "de", "Hallo", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := CheckLanguage(tc.want, tc.text)
			if ok != tc.ok {
				t.Fatalf("got %t (%s), want %t", ok, why, tc.ok)
			}
			if !ok && why == "" {
				t.Fatal("a failure with no reason")
			}
		})
	}
}
