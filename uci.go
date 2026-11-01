package main

import (
	"bufio"
	"strings"
)

// We parse UCI ourselves rather than shelling to `uci show`, so the whole configuration path is
// testable on a workstation with no uci binary. The `uci` binary is used only to write.

type uciSection struct {
	Type    string
	Name    string
	Options map[string]string
	Lists   map[string][]string
}

func parseUCI(sc *bufio.Scanner) []uciSection {
	var out []uciSection
	var cur *uciSection
	for sc.Scan() {
		fields := splitUCI(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "config":
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &uciSection{Options: map[string]string{}, Lists: map[string][]string{}}
			if len(fields) > 1 {
				cur.Type = fields[1]
			}
			if len(fields) > 2 {
				cur.Name = fields[2]
			}
		case "option":
			if cur != nil && len(fields) > 2 {
				cur.Options[fields[1]] = fields[2]
			}
		case "list":
			if cur != nil && len(fields) > 2 {
				cur.Lists[fields[1]] = append(cur.Lists[fields[1]], fields[2])
			}
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// splitUCI tokenises one line, honouring single and double quotes. A # outside quotes starts a
// comment wherever it stands, so a value containing # must be quoted, as uci itself writes it.
func splitUCI(line string) []string {
	var fields []string
	var buf strings.Builder
	var quote rune
	inField := false
	for _, c := range line {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				buf.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, inField = c, true
		case c == ' ' || c == '\t':
			if inField {
				fields = append(fields, buf.String())
				buf.Reset()
				inField = false
			}
		case c == '#':
			if inField {
				fields = append(fields, buf.String())
			}
			return fields
		default:
			buf.WriteRune(c)
			inField = true
		}
	}
	if inField {
		fields = append(fields, buf.String())
	}
	return fields
}
