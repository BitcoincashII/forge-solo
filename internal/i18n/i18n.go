// Package i18n provides internationalization support
package i18n

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Translator handles message translations
type Translator struct {
	defaultLang string
	messages    map[string]map[string]string // lang -> key -> message
	mu          sync.RWMutex
}

// New creates a new Translator
func New(defaultLang string) *Translator {
	return &Translator{
		defaultLang: defaultLang,
		messages:    make(map[string]map[string]string),
	}
}

// LoadDir loads all JSON translation files from a directory
func (t *Translator) LoadDir(dir string) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read i18n directory: %w", err)
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		lang := strings.TrimSuffix(file.Name(), ".json")
		path := filepath.Join(dir, file.Name())

		if err := t.LoadFile(lang, path); err != nil {
			return fmt.Errorf("failed to load %s: %w", path, err)
		}
	}

	return nil
}

// LoadFile loads a translation file for a specific language
func (t *Translator) LoadFile(lang, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var messages map[string]string
	if err := json.Unmarshal(data, &messages); err != nil {
		return fmt.Errorf("invalid JSON in %s: %w", path, err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.messages[lang] == nil {
		t.messages[lang] = make(map[string]string)
	}

	for k, v := range messages {
		t.messages[lang][k] = v
	}

	return nil
}

// T translates a key to the specified language
func (t *Translator) T(lang, key string, args ...interface{}) string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Try requested language
	if msgs, ok := t.messages[lang]; ok {
		if msg, ok := msgs[key]; ok {
			if len(args) > 0 {
				return fmt.Sprintf(msg, args...)
			}
			return msg
		}
	}

	// Fall back to default language
	if lang != t.defaultLang {
		if msgs, ok := t.messages[t.defaultLang]; ok {
			if msg, ok := msgs[key]; ok {
				if len(args) > 0 {
					return fmt.Sprintf(msg, args...)
				}
				return msg
			}
		}
	}

	// Return key if not found
	return key
}

// HasLanguage checks if a language is loaded
func (t *Translator) HasLanguage(lang string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.messages[lang]
	return ok
}

// Languages returns all loaded languages
func (t *Translator) Languages() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var langs []string
	for lang := range t.messages {
		langs = append(langs, lang)
	}
	return langs
}

// DefaultLang returns the default language
func (t *Translator) DefaultLang() string {
	return t.defaultLang
}

// SupportedLanguages is a map of language codes to display names
var SupportedLanguages = map[string]string{
	"en": "English",
	"es": "Espanol",
	"zh": "中文",
	"ru": "Русский",
	"pt": "Portugues",
	"ja": "日本語",
	"ko": "한국어",
	"de": "Deutsch",
	"fr": "Francais",
}
