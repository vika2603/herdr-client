package main

import (
	"regexp"
	"strings"
)

// A second, independent reading of the same relation, out of herdr's own
// tests. A round-trip test that names one method and one result is evidence
// that the two go together, but only weak evidence: a test may name a method
// it merely sets up with. Measured against herdr 0.9.1 this reading covers 28
// of 103 methods and gets 2 of them wrong, so it is never an answer here,
// only a second opinion that disagrees out loud when the handler scan and it
// part ways.

// pair is a method and the result type a test names beside it.
type pair struct {
	result string
	at     location
}

var testFnRe = regexp.MustCompile(`#\[test\]\s*(?:#\[[^\]]*\]\s*)*fn\s+([a-z_][A-Za-z0-9_]*)`)

// testPairs reads one result type per Method variant out of the test
// functions that name exactly one of each.
func testPairs(files []source) map[string]pair {
	pairs := make(map[string]pair)
	ambiguous := make(map[string]bool)
	for _, file := range files {
		text := file.tests()
		if text == "" {
			continue
		}
		for _, m := range testFnRe.FindAllStringSubmatchIndex(text, -1) {
			open := strings.IndexByte(text[m[1]:], '{')
			if open < 0 {
				continue
			}
			body, _, ok := blockAt(text, m[1]+open)
			if !ok {
				continue
			}
			variants := distinct(methodVariantAt, body)
			results := distinct(responseRe, body)
			if len(variants) != 1 || len(results) != 1 {
				continue
			}
			variant, result := variants[0], snakeCase(results[0])
			if existing, seen := pairs[variant]; seen && existing.result != result {
				// Two tests naming different results for one method say
				// nothing, so the variant drops out rather than picking one.
				ambiguous[variant] = true
				continue
			}
			pairs[variant] = pair{result: result, at: location{file: file.path, line: lineAt(text, m[0])}}
		}
	}
	for variant := range ambiguous {
		delete(pairs, variant)
	}
	return pairs
}

func distinct(re *regexp.Regexp, body string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}
