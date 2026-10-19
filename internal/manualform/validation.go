// Package manualform validates the Telegram form against the Channel API schema.
package manualform

import (
	"errors"
	"fmt"
	"math"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

var (
	ErrRequired  = errors.New("required")
	ErrValue     = errors.New("invalid value")
	ErrSchema    = errors.New("unsupported form schema")
	phonePattern = regexp.MustCompile(`^\+?[0-9\-()\s]{6,20}$`)
)

func CheckSchema(schema dujiao.ManualFormSchema) error {
	seen := make(map[string]bool)
	if len(schema.Fields) > 30 {
		return ErrSchema
	}
	for _, field := range schema.Fields {
		if field.Key == "" || seen[field.Key] {
			return ErrSchema
		}
		seen[field.Key] = true
		switch field.Type {
		case "text", "textarea", "email", "phone", "number":
		case "select", "radio", "checkbox":
			if len(field.Options) == 0 || len(field.Options) > 50 {
				return ErrSchema
			}
		default:
			return ErrSchema
		}
		if field.Regex != "" {
			if _, err := compilePattern(field.Regex); err != nil {
				return ErrSchema
			}
		}
	}
	return nil
}

// Value returns nil for a skipped optional field. Numbers and checkbox arrays
// retain their JSON types instead of being sent as strings.
func Value(field dujiao.ManualFormField, input any) (any, error) {
	if input == nil || input == "" {
		if field.Required {
			return nil, ErrRequired
		}
		return nil, nil
	}
	switch field.Type {
	case "text", "textarea", "email", "phone", "select", "radio":
		value, ok := input.(string)
		if !ok {
			return nil, ErrValue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return Value(field, nil)
		}
		if utf8.RuneCountInString(value) > 2000 || (field.MaxLen != nil && utf8.RuneCountInString(value) > *field.MaxLen) {
			return nil, ErrValue
		}
		switch field.Type {
		case "email":
			if _, err := mail.ParseAddress(value); err != nil {
				return nil, ErrValue
			}
		case "phone":
			if !phonePattern.MatchString(value) {
				return nil, ErrValue
			}
		case "select", "radio":
			if !slices.Contains(field.Options, value) {
				return nil, ErrValue
			}
		}
		if field.Regex != "" {
			pattern, err := compilePattern(field.Regex)
			if err != nil {
				return nil, ErrSchema
			}
			if !pattern.MatchString(value) {
				return nil, ErrValue
			}
		}
		return value, nil
	case "number":
		var value float64
		switch input := input.(type) {
		case string:
			var err error
			value, err = strconv.ParseFloat(strings.TrimSpace(input), 64)
			if err != nil {
				return nil, ErrValue
			}
		case float64:
			value = input
		default:
			return nil, ErrValue
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || (field.Min != nil && value < *field.Min) || (field.Max != nil && value > *field.Max) {
			return nil, ErrValue
		}
		return value, nil
	case "checkbox":
		var values []string
		switch input := input.(type) {
		case []string:
			values = slices.Clone(input)
		case []any:
			for _, entry := range input {
				value, ok := entry.(string)
				if !ok {
					return nil, ErrValue
				}
				values = append(values, value)
			}
		default:
			return nil, ErrValue
		}
		if len(values) == 0 {
			return Value(field, nil)
		}
		for _, value := range values {
			if !slices.Contains(field.Options, value) {
				return nil, ErrValue
			}
		}
		slices.Sort(values)
		return slices.Compact(values), nil
	default:
		return nil, ErrSchema
	}
}

func Normalize(schema dujiao.ManualFormSchema, input map[string]any) (map[string]any, error) {
	if err := CheckSchema(schema); err != nil {
		return nil, err
	}
	result := make(map[string]any)
	for _, field := range schema.Fields {
		value, err := Value(field, input[field.Key])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.Key, err)
		}
		if value != nil {
			result[field.Key] = value
		}
	}
	return result, nil
}

func ItemKey(productID, skuID uint) string { return fmt.Sprintf("%d:%d", productID, skuID) }

func compilePattern(raw string) (*regexp.Regexp, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") {
		end := -1
		for i := len(raw) - 1; i > 0; i-- {
			if raw[i] != '/' {
				continue
			}
			escapes := 0
			for j := i - 1; j >= 0 && raw[j] == '\\'; j-- {
				escapes++
			}
			if escapes%2 == 0 {
				end = i
				break
			}
		}
		if end > 0 {
			flags, pattern := raw[end+1:], raw[1:end]
			prefix := ""
			for _, flag := range flags {
				switch flag {
				case 'i', 'm', 's':
					prefix += "(?" + string(flag) + ")"
				case 'g', 'u', 'y':
				default:
					return nil, ErrSchema
				}
			}
			raw = prefix + pattern
		}
	}
	return regexp.Compile(raw)
}
