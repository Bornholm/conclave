package prompt

import (
	"bytes"
	"strings"
)

// maxIndexedFiles caps the index given in the prompt: past that, the index
// itself would fill the context the diff file is meant to spare.
const maxIndexedFiles = 400

// DiffSection locates the diff of one file in the diff file.
type DiffSection struct {
	Path string
	// StartLine and EndLine are 1-based and inclusive.
	StartLine int
	EndLine   int
}

// Lines is the length of the section.
func (s DiffSection) Lines() int { return s.EndLine - s.StartLine + 1 }

// DiffIndex describes a diff given to reviewers as a file.
type DiffIndex struct {
	Lines    int
	Bytes    int
	Sections []DiffSection
	// Omitted counts the files left out of Sections.
	Omitted int
}

// IndexDiff splits a unified diff on its "diff --git" headers.
func IndexDiff(diff []byte) DiffIndex {
	idx := DiffIndex{Bytes: len(diff)}
	var sections []DiffSection
	for i, line := range bytes.Split(bytes.TrimSuffix(diff, []byte("\n")), []byte("\n")) {
		n := i + 1
		idx.Lines = n
		if !bytes.HasPrefix(line, []byte("diff --git ")) {
			continue
		}
		if len(sections) > 0 {
			sections[len(sections)-1].EndLine = n - 1
		}
		sections = append(sections, DiffSection{Path: diffPath(string(line)), StartLine: n})
	}
	if len(sections) > 0 {
		sections[len(sections)-1].EndLine = idx.Lines
	}
	if len(sections) > maxIndexedFiles {
		idx.Omitted = len(sections) - maxIndexedFiles
		sections = sections[:maxIndexedFiles]
	}
	idx.Sections = sections
	return idx
}

// diffPath reads the new path from a "diff --git a/<old> b/<new>" header. A
// path with spaces makes the header ambiguous; taking what follows the last
// " b/" is right unless the new path itself contains " b/". Quoted paths
// keep their quotes: the index is a map, not an API.
func diffPath(header string) string {
	rest := strings.TrimPrefix(header, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+len(" b/"):]
	}
	if i := strings.LastIndex(rest, ` "b/`); i >= 0 {
		return `"` + rest[i+len(` "b/`):]
	}
	return rest
}
