package billing

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Neutral words in Russian that are NOT financial or service-related
var neutralWords = []string{
	"солнце", "луна", "звезда", "облако", "ветер",
	"река", "море", "гора", "лес", "поле",
	"птица", "рыба", "цветок", "камень", "песок",
	"книга", "карта", "флаг", "корабль", "дорога",
	"окно", "дверь", "стена", "крыша", "фундамент",
	"время", "пространство", "движение", "покой", "тишина",
	"лето", "зима", "весна", "осень", "день",
	"ночь", "утро", "вечер", "сумерки", "рассвет",
	"золото", "серебро", "бронза", "алмаз", "жемчуг",
	"чай", "кофе", "хлеб", "соль", "сахар",
}

// GeneratePaymentComment generates a unique neutral payment comment
// Format: 2-3 random Russian words + short alphanumeric suffix
// The comment MUST NOT contain financial or service-related words
func GeneratePaymentComment() (string, error) {
	// Select 2-3 random words (70% chance of 2 words, 30% chance of 3 words)
	numWords := 2
	if randInt(100) < 30 {
		numWords = 3
	}

	var words []string
	usedIndices := make(map[int]bool)

	for len(words) < numWords {
		idx := randInt(len(neutralWords))
		if !usedIndices[idx] {
			words = append(words, neutralWords[idx])
			usedIndices[idx] = true
		}
	}

	// Generate short alphanumeric suffix (4-5 characters)
	suffixLen := 4
	if randInt(100) < 50 {
		suffixLen = 5
	}

	suffix, err := generateSuffix(suffixLen)
	if err != nil {
		return "", fmt.Errorf("failed to generate suffix: %w", err)
	}

	// Combine words and suffix
	comment := words[0]
	for i := 1; i < len(words); i++ {
		comment += " " + words[i]
	}
	comment += " " + suffix

	return comment, nil
}

// generateSuffix generates a short alphanumeric suffix
func generateSuffix(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[num.Int64()]
	}
	return string(b), nil
}

// randInt generates a random integer in range [0, max)
func randInt(max int) int {
	if max <= 0 {
		return 0
	}
	num, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(num.Int64())
}

