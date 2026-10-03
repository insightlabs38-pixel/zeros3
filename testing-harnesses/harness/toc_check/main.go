// toc_check validates that the "Source map" / "Test map" comment block at
// the top of zeros3.go or zeros3_test.go still points at the lines it
// claims to. It is pure static analysis: it never builds or runs zeros3.
//
// Usage:
//
//	go run ./harness/toc_check -file ../zeros3.go
//	go run ./harness/toc_check -file ../zeros3_test.go
//
// Exit status is 0 and a PASS summary is printed if every entry's claimed
// line number matches the file; otherwise it exits 1 and prints exactly
// which entry disagrees with the file and how.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var (
	mapHeaderRe = regexp.MustCompile(`^//\s*(Source map|Test map)\s*$`)
	barRe       = regexp.MustCompile(`^//\s*=+\s*$`)
	entryRe     = regexp.MustCompile(`^//\s+(\d+)\s+(\S.*\S|\S)\s*$`)
)

type entry struct {
	line int
	desc string
}

func main() {
	filePath := flag.String("file", "", "path to zeros3.go or zeros3_test.go")
	flag.Parse()
	if *filePath == "" {
		fmt.Fprintln(os.Stderr, "toc_check: -file is required")
		os.Exit(2)
	}

	lines, err := readLines(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "toc_check: %v\n", err)
		os.Exit(2)
	}

	mapName, entries, err := extractMap(lines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "toc_check: %v\n", err)
		os.Exit(1)
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "toc_check: found a map header but parsed zero entries")
		os.Exit(1)
	}

	var problems []string

	// Every claimed line number must exist in the file and must be
	// strictly increasing (the map lists sections top-to-bottom).
	prev := 0
	for i, e := range entries {
		if e.line < 1 || e.line > len(lines) {
			problems = append(problems, fmt.Sprintf(
				"entry %d (%q): claimed line %d is out of range (file has %d lines)",
				i+1, e.desc, e.line, len(lines)))
			continue
		}
		if e.line <= prev {
			problems = append(problems, fmt.Sprintf(
				"entry %d (%q): claimed line %d is not after the previous entry's line %d",
				i+1, e.desc, e.line, prev))
		}
		prev = e.line

		// The first entry anchors wherever the file's own content
		// starts (the package doc comment in zeros3.go; TestMain's
		// doc comment in zeros3_test.go, after TestMain's imports),
		// which is not itself a "// N. Title" section header. Every
		// other entry must anchor a real section header, which is
		// always immediately preceded by a "// ===...===" divider
		// line (the project's established section-header convention).
		// This is what actually catches drift: if a later edit inserts
		// or removes lines above a section without updating the map,
		// the divider will no longer sit exactly one line above the
		// claimed line.
		if i == 0 {
			if strings.TrimSpace(lines[e.line-1]) == "" {
				problems = append(problems, fmt.Sprintf(
					"entry 1 (%q): claimed line %d is blank", e.desc, e.line))
			}
			continue
		}
		aboveIdx := e.line - 2 // 0-indexed line directly above e.line
		if aboveIdx < 0 || !barRe.MatchString(lines[aboveIdx]) {
			got := ""
			if aboveIdx >= 0 && aboveIdx < len(lines) {
				got = strings.TrimSpace(lines[aboveIdx])
			}
			problems = append(problems, fmt.Sprintf(
				"entry %d (%q): line %d is not immediately preceded by a section-header divider (\"// ===...\"); found %q instead",
				i+1, e.desc, e.line, got))
		}
	}

	if len(problems) > 0 {
		fmt.Printf("FAIL: %s in %s -- %d of %d entries disagree with the file:\n", mapName, *filePath, len(problems), len(entries))
		for _, p := range problems {
			fmt.Println("  - " + p)
		}
		os.Exit(1)
	}

	fmt.Printf("PASS: %s in %s -- all %d entries point at their claimed lines\n", mapName, *filePath, len(entries))
}

// extractMap finds the "// Source map" or "// Test map" comment block and
// parses its "//   LINE    Description" rows into entries, in file order.
func extractMap(lines []string) (string, []entry, error) {
	start := -1
	var mapName string
	for i, l := range lines {
		if m := mapHeaderRe.FindStringSubmatch(l); m != nil {
			start = i
			mapName = m[1]
			break
		}
	}
	if start == -1 {
		return "", nil, fmt.Errorf("no \"// Source map\" or \"// Test map\" header found")
	}

	// The block is bounded above and below by "// ===...===" divider
	// lines; the header itself sits just below the opening divider.
	// Scan forward from the header to the block's closing divider,
	// collecting every "//   NNN   description" row in between.
	var entries []entry
	inRows := false
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if barRe.MatchString(l) {
			if inRows {
				return mapName, entries, nil
			}
			// This is the divider between the header and the
			// "Lines / Subsystem" column labels; keep scanning.
			continue
		}
		if m := entryRe.FindStringSubmatch(l); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			entries = append(entries, entry{line: n, desc: strings.TrimSpace(m[2])})
			inRows = true
			continue
		}
	}
	return "", nil, fmt.Errorf("map header found at line %d but no closing \"// ===...===\" divider after it", start+1)
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}
