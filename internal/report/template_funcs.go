package report

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The template subset's functions that text/template's builtins of the same
// name do not implement as the subset states, over the values a report
// document decodes to: null, a boolean, an integer, a string, an array and an
// object. An integer is an int64 from the document, or an int from a constant
// or from len.

// kindOf names the kind of a template value, as a failure names it.
func kindOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case int, int64:
		return "an integer"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return fmt.Sprintf("a %T", value)
	}
}

// integer is value as an integer, and whether it is one.
func integer(value any) (int64, bool) {
	switch n := value.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// length is the number of bytes of a string, of elements of an array and of
// members of an object.
func length(value any) (int, error) {
	switch v := value.(type) {
	case string:
		return len(v), nil
	case []any:
		return len(v), nil
	case map[string]any:
		return len(v), nil
	default:
		return 0, fmt.Errorf("len of %s", kindOf(value))
	}
}

// index reads value[keys[0]][keys[1]]…: an integer key an array's element, a
// string key an object's member. A key outside the array, a member the
// object does not carry and any other key fail.
func index(value any, keys ...any) (any, error) {
	for _, key := range keys {
		switch v := value.(type) {
		case []any:
			at, isInteger := integer(key)
			if !isInteger || at < 0 || at >= int64(len(v)) {
				return nil, fmt.Errorf("index %v of an array of %d elements", key, len(v))
			}
			value = v[at]
		case map[string]any:
			name, isString := key.(string)
			member, carried := v[name]
			if !isString || !carried {
				return nil, fmt.Errorf("index %v of an object that carries no such member", key)
			}
			value = member
		default:
			return nil, fmt.Errorf("index of %s", kindOf(value))
		}
	}
	return value, nil
}

// equalPair is whether two integers, two strings or two booleans are equal.
// Any other pair fails.
func equalPair(a, b any) (bool, error) {
	if x, isInteger := integer(a); isInteger {
		if y, alsoInteger := integer(b); alsoInteger {
			return x == y, nil
		}
	}
	switch x := a.(type) {
	case string:
		if y, isString := b.(string); isString {
			return x == y, nil
		}
	case bool:
		if y, isBool := b.(bool); isBool {
			return x == y, nil
		}
	}
	return false, fmt.Errorf("compare %s with %s", kindOf(a), kindOf(b))
}

// equal is whether a equals any later operand.
func equal(a any, later ...any) (bool, error) {
	if len(later) == 0 {
		return false, errors.New("eq needs an operand to compare with")
	}
	for _, b := range later {
		same, err := equalPair(a, b)
		if err != nil || same {
			return same, err
		}
	}
	return false, nil
}

// notEqual is whether two operands differ.
func notEqual(a, b any) (bool, error) {
	same, err := equalPair(a, b)
	return !same, err
}

// ordered is the comparison whose result holds of compare(a, b) as test
// says: two integers as numbers, two strings by their bytes. Any other pair
// fails.
func ordered(test func(int) bool) func(a, b any) (bool, error) {
	return func(a, b any) (bool, error) {
		if x, isInteger := integer(a); isInteger {
			if y, alsoInteger := integer(b); alsoInteger {
				return test(cmp.Compare(x, y)), nil
			}
		}
		if x, isString := a.(string); isString {
			if y, alsoString := b.(string); alsoString {
				return test(strings.Compare(x, y)), nil
			}
		}
		return false, fmt.Errorf("order %s against %s", kindOf(a), kindOf(b))
	}
}

// rangeable is a value a range visits: an array, an object, or null, which
// runs the range's else branch. A range over any other value fails.
func rangeable(value any) (any, error) {
	switch value.(type) {
	case nil, []any, map[string]any:
		return value, nil
	default:
		return nil, fmt.Errorf("range over %s", kindOf(value))
	}
}

// printf writes format with each verb replaced by the next operand. A verb is
// a percent sign and one of v, s, d, t, q and %; any other, a verb with no
// operand left and an operand no verb takes fail.
func printf(format string, operands ...any) (string, error) {
	var out strings.Builder
	next := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			out.WriteByte(format[i])
			continue
		}
		i++
		switch {
		case i == len(format):
			return "", errors.New("printf: the format ends in a lone %")
		case format[i] == '%':
			out.WriteByte('%')
			continue
		case next == len(operands):
			return "", fmt.Errorf("printf: no operand is left for %%%c", format[i])
		}
		written, err := verb(format[i], operands[next])
		if err != nil {
			return "", err
		}
		out.WriteString(written)
		next++
	}
	if next < len(operands) {
		return "", fmt.Errorf("printf: %d operands, and the format takes %d", len(operands), next)
	}
	return out.String(), nil
}

// verb is one operand written under one printf verb.
func verb(c byte, operand any) (string, error) {
	switch c {
	case 'v', 's':
		return fmt.Sprint(operand), nil
	case 'q':
		return quoted(fmt.Sprint(operand)), nil
	case 'd':
		if n, isInteger := integer(operand); isInteger {
			return strconv.FormatInt(n, 10), nil
		}
	case 't':
		if b, isBool := operand.(bool); isBool {
			return strconv.FormatBool(b), nil
		}
	default:
		return "", fmt.Errorf("printf: %%%c is outside the template subset", c)
	}
	return "", fmt.Errorf("printf: %%%c of %s", c, kindOf(operand))
}

// controlEscapes are the control characters %q writes as a letter escape.
var controlEscapes = map[rune]string{
	'\a': `\a`, '\b': `\b`, '\f': `\f`, '\n': `\n`, '\r': `\r`, '\t': `\t`, '\v': `\v`,
}

// quoted is text between double quotes, with a quote and a backslash escaped,
// the control characters controlEscapes names written as their escapes, every
// other character below U+0020 and U+007F written as \x and two lowercase
// hexadecimal digits, and every other character written as itself.
func quoted(text string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range text {
		switch escape, named := controlEscapes[r]; {
		case r == '"' || r == '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case named:
			out.WriteString(escape)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&out, `\x%02x`, r)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}
