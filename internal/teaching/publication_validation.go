package teaching

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Publication bodies are deliberately bounded so validation remains linear in
// input size and cannot consume unbounded parser state.
const (
	maxPublicationBodyRunes        = 200_000
	maxPublicationMathRunes        = 8_192
	maxPublicationMathCommands     = 1_024
	maxPublicationBraceDepth       = 64
	maxPublicationEnvironmentDepth = 32
	maxPublicationEnvironmentName  = 64
)

var errInvalidPublicationBody = errors.New("invalid publication body")

// validatePublicationBody performs one bounded structural pass over Markdown
// and the supported LaTeX delimiters. It is intentionally not a renderer.
func validatePublicationBody(body string) error {
	if !utf8.ValidString(body) || utf8.RuneCountInString(body) > maxPublicationBodyRunes {
		return errInvalidPublicationBody
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "javascript:") {
		return errInvalidPublicationBody
	}

	runes := []rune(body)
	lineStart := true
	var fence rune
	fenceLength := 0
	inlineTicks := 0
	braceDepth := 0
	environments := make([]string, 0, maxPublicationEnvironmentDepth)
	mathDelimiter := ""
	mathRunes, mathCommands := 0, 0
	mathBraceBaseline, mathEnvironmentBaseline := 0, 0

	for i := 0; i < len(runes); {
		r := runes[i]
		if isDisallowedPublicationControl(r) {
			return errInvalidPublicationBody
		}

		if lineStart && inlineTicks == 0 {
			marker, length, end := publicationFenceMarker(runes, i)
			if fence == 0 && length >= 3 {
				fence, fenceLength = marker, length
				i, lineStart = end, false
				continue
			}
			if fence != 0 && marker == fence && length >= fenceLength && publicationFenceTailIsBlank(runes, end) {
				fence, fenceLength = 0, 0
				i, lineStart = end, false
				continue
			}
		}
		if fence != 0 {
			lineStart = r == '\n' || r == '\r'
			i++
			continue
		}

		if r == '`' {
			count := publicationRunLength(runes, i, '`')
			if inlineTicks == 0 {
				inlineTicks = count
			} else if count == inlineTicks {
				inlineTicks = 0
			}
			i += count
			lineStart = false
			continue
		}
		if inlineTicks != 0 {
			lineStart = r == '\n' || r == '\r'
			i++
			continue
		}

		if r == '\n' || r == '\r' {
			lineStart = true
			if mathDelimiter == "$" {
				return errInvalidPublicationBody
			}
			i++
			continue
		}
		lineStart = false

		if r == '\\' {
			if i+1 >= len(runes) {
				return errInvalidPublicationBody
			}
			next := runes[i+1]
			if isDisallowedPublicationControl(next) {
				return errInvalidPublicationBody
			}
			if next == '\n' || next == '\r' {
				if mathDelimiter == "$" {
					return errInvalidPublicationBody
				}
				consumed := 2
				if next == '\r' && i+2 < len(runes) && runes[i+2] == '\n' {
					consumed++
				}
				if mathDelimiter != "" {
					mathRunes += consumed
					if mathRunes > maxPublicationMathRunes {
						return errInvalidPublicationBody
					}
				}
				i += consumed
				lineStart = true
				continue
			}
			if next == '(' || next == '[' {
				delimiter := "\\("
				if next == '[' {
					delimiter = "\\["
				}
				if mathDelimiter != "" {
					return errInvalidPublicationBody
				}
				mathDelimiter, mathRunes, mathCommands = delimiter, 0, 0
				mathBraceBaseline, mathEnvironmentBaseline = braceDepth, len(environments)
				i += 2
				continue
			}
			if next == ')' || next == ']' {
				want := "\\("
				if next == ']' {
					want = "\\["
				}
				if mathDelimiter != want || braceDepth != mathBraceBaseline || len(environments) != mathEnvironmentBaseline {
					return errInvalidPublicationBody
				}
				mathDelimiter = ""
				i += 2
				continue
			}
			if isASCIILetter(next) {
				end := i + 2
				for end < len(runes) && isASCIILetter(runes[end]) {
					end++
				}
				command := string(runes[i+1 : end])
				if command == "write" || command == "input" || command == "include" {
					return errInvalidPublicationBody
				}
				if command == "begin" || command == "end" {
					name, after, ok := publicationEnvironmentName(runes, end)
					if !ok {
						return errInvalidPublicationBody
					}
					if mathDelimiter != "" {
						mathRunes += after - i
						mathCommands++
						if mathRunes > maxPublicationMathRunes || mathCommands > maxPublicationMathCommands {
							return errInvalidPublicationBody
						}
					}
					if command == "begin" {
						if len(environments) >= maxPublicationEnvironmentDepth {
							return errInvalidPublicationBody
						}
						environments = append(environments, name)
					} else {
						if len(environments) == 0 || environments[len(environments)-1] != name {
							return errInvalidPublicationBody
						}
						environments = environments[:len(environments)-1]
					}
					i = after
					continue
				}
				if mathDelimiter != "" {
					mathRunes += end - i
					mathCommands++
					if mathRunes > maxPublicationMathRunes || mathCommands > maxPublicationMathCommands {
						return errInvalidPublicationBody
					}
				}
				i = end
				continue
			}
			if mathDelimiter != "" {
				mathRunes += 2
				if mathRunes > maxPublicationMathRunes {
					return errInvalidPublicationBody
				}
			}
			i += 2
			continue
		}

		if r == '$' {
			count := publicationRunLength(runes, i, '$')
			if count > 2 {
				return errInvalidPublicationBody
			}
			delimiter := "$"
			if count == 2 {
				delimiter = "$$"
			}
			if mathDelimiter == "" {
				mathDelimiter, mathRunes, mathCommands = delimiter, 0, 0
				mathBraceBaseline, mathEnvironmentBaseline = braceDepth, len(environments)
			} else if mathDelimiter == delimiter {
				if braceDepth != mathBraceBaseline || len(environments) != mathEnvironmentBaseline {
					return errInvalidPublicationBody
				}
				mathDelimiter = ""
			} else {
				return errInvalidPublicationBody
			}
			i += count
			continue
		}

		if r == '{' {
			braceDepth++
			if braceDepth > maxPublicationBraceDepth {
				return errInvalidPublicationBody
			}
		} else if r == '}' {
			braceDepth--
			if braceDepth < 0 {
				return errInvalidPublicationBody
			}
		}
		if mathDelimiter != "" {
			mathRunes++
			if mathRunes > maxPublicationMathRunes {
				return errInvalidPublicationBody
			}
		}
		i++
	}

	if fence != 0 || inlineTicks != 0 || braceDepth != 0 || len(environments) != 0 || mathDelimiter != "" {
		return errInvalidPublicationBody
	}
	return nil
}

func publicationFenceMarker(runes []rune, start int) (rune, int, int) {
	i := start
	spaces := 0
	for i < len(runes) && runes[i] == ' ' && spaces < 4 {
		i++
		spaces++
	}
	if spaces > 3 || i >= len(runes) || (runes[i] != '`' && runes[i] != '~') {
		return 0, 0, start
	}
	marker := runes[i]
	length := publicationRunLength(runes, i, marker)
	return marker, length, i + length
}

func publicationFenceTailIsBlank(runes []rune, start int) bool {
	for i := start; i < len(runes) && runes[i] != '\n' && runes[i] != '\r'; i++ {
		if runes[i] != ' ' && runes[i] != '\t' {
			return false
		}
	}
	return true
}

func publicationRunLength(runes []rune, start int, want rune) int {
	i := start
	for i < len(runes) && runes[i] == want {
		i++
	}
	return i - start
}

func publicationEnvironmentName(runes []rune, start int) (string, int, bool) {
	if start >= len(runes) || runes[start] != '{' {
		return "", start, false
	}
	i := start + 1
	nameStart := i
	for i < len(runes) && (isASCIILetter(runes[i]) || runes[i] == '*') && i-nameStart <= maxPublicationEnvironmentName {
		i++
	}
	if i == nameStart || i >= len(runes) || runes[i] != '}' || i-nameStart > maxPublicationEnvironmentName {
		return "", start, false
	}
	return string(runes[nameStart:i]), i + 1, true
}

func isASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func isDisallowedPublicationControl(r rune) bool {
	return isDisallowedTextControl(r, true)
}

func containsDisallowedTextControl(value string, allowLayoutWhitespace bool) bool {
	for _, r := range value {
		if isDisallowedTextControl(r, allowLayoutWhitespace) {
			return true
		}
	}
	return false
}

func isDisallowedTextControl(r rune, allowLayoutWhitespace bool) bool {
	if !unicode.IsControl(r) {
		return false
	}
	return !allowLayoutWhitespace || r != '\n' && r != '\r' && r != '\t'
}
