package manualform

import (
	"errors"
	"reflect"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestFieldValidation(t *testing.T) {
	minimum, maximum, maxLen := 1.0, 5.0, 3
	tests := []struct {
		name  string
		field dujiao.ManualFormField
		input any
		want  any
		err   error
	}{
		{"required", dujiao.ManualFormField{Type: "text", Required: true}, "  ", nil, ErrRequired},
		{"skip", dujiao.ManualFormField{Type: "text"}, nil, nil, nil},
		{"unicode length", dujiao.ManualFormField{Type: "textarea", MaxLen: &maxLen}, "测试中文", nil, ErrValue},
		{"multiline", dujiao.ManualFormField{Type: "textarea"}, " 一\n二 ", "一\n二", nil},
		{"regex flags", dujiao.ManualFormField{Type: "text", Regex: `/^abc$/i`}, "ABC", "ABC", nil},
		{"regex mismatch", dujiao.ManualFormField{Type: "text", Regex: `^[a-z]+$`}, "123", nil, ErrValue},
		{"email", dujiao.ManualFormField{Type: "email"}, "demo@example.com", "demo@example.com", nil},
		{"invalid email", dujiao.ManualFormField{Type: "email"}, "invalid", nil, ErrValue},
		{"phone", dujiao.ManualFormField{Type: "phone"}, "+86 13800138000", "+86 13800138000", nil},
		{"invalid phone", dujiao.ManualFormField{Type: "phone"}, "abc", nil, ErrValue},
		{"number", dujiao.ManualFormField{Type: "number", Min: &minimum, Max: &maximum}, "2.5", 2.5, nil},
		{"out of range", dujiao.ManualFormField{Type: "number", Min: &minimum, Max: &maximum}, "6", nil, ErrValue},
		{"nonfinite", dujiao.ManualFormField{Type: "number"}, "NaN", nil, ErrValue},
		{"select", dujiao.ManualFormField{Type: "select", Options: []string{"a", "b"}}, "b", "b", nil},
		{"invalid radio", dujiao.ManualFormField{Type: "radio", Options: []string{"a"}}, "b", nil, ErrValue},
		{"checkbox", dujiao.ManualFormField{Type: "checkbox", Options: []string{"a", "b"}}, []string{"b", "a", "b"}, []string{"a", "b"}, nil},
		{"checkbox json", dujiao.ManualFormField{Type: "checkbox", Options: []string{"a"}}, []any{"a"}, []string{"a"}, nil},
		{"required checkbox", dujiao.ManualFormField{Type: "checkbox", Required: true, Options: []string{"a"}}, []string{}, nil, ErrRequired},
		{"invalid checkbox", dujiao.ManualFormField{Type: "checkbox", Options: []string{"a"}}, []string{"b"}, nil, ErrValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Value(test.field, test.input)
			if !errors.Is(err, test.err) || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Value = %#v, %v; want %#v, %v", got, err, test.want, test.err)
			}
		})
	}
}

func TestNormalizeRejectsChangedSchemaAndDropsExtraFields(t *testing.T) {
	schema := dujiao.ManualFormSchema{Fields: []dujiao.ManualFormField{{Key: "name", Type: "text", Required: true}}}
	values, err := Normalize(schema, map[string]any{"name": "demo", "unexpected": "value"})
	if err != nil || len(values) != 1 {
		t.Fatalf("normalized = %#v, %v", values, err)
	}
	schema.Fields = append(schema.Fields, dujiao.ManualFormField{Key: "email", Type: "email", Required: true})
	if _, err := Normalize(schema, values); !errors.Is(err, ErrRequired) {
		t.Fatalf("missing new required field: %v", err)
	}
}
