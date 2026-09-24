package document

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Issue struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Source   string   `json:"source"`
	Message  string   `json:"message"`
}

type Link struct {
	Target string
	Image  bool
}

type Heading struct {
	Level int
	ID    string
	Text  string
}

type Document struct {
	SourceKey    string
	Path         string
	Title        string
	SourceHash   string
	Headings     []Heading
	Links        []Link
	TableColumns []int
}

type Graph struct {
	Root         string
	EntryKey     string
	Documents    []*Document
	ByKey        map[string]*Document
	Issues       []Issue
	Policy       Policy
	AllowedRoots []string
	PathKeys     map[string]string
}

type Policy struct {
	Title                    string
	HeadingJumps             string
	UnpublishedMarkdownLinks string
	NamingSource             string
	NameMapping              map[string]string
	ImageTypes               map[string]bool
	MaxTableColumns          int
}

func (g *Graph) HasErrors() bool {
	for _, issue := range g.Issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (g *Graph) SortIssues() {
	sort.SliceStable(g.Issues, func(i, j int) bool {
		if g.Issues[i].Source != g.Issues[j].Source {
			return g.Issues[i].Source < g.Issues[j].Source
		}
		if g.Issues[i].Severity != g.Issues[j].Severity {
			return g.Issues[i].Severity < g.Issues[j].Severity
		}
		return g.Issues[i].Code < g.Issues[j].Code
	})
}

func SourceHash(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
