package validation

import "sort"

// Collector retains the canonical first errors while bounding memory for admission checks.
type Collector struct {
	errors    []Diagnostic
	truncated bool
}

func (c *Collector) Add(finding Diagnostic) {
	index := sort.Search(len(c.errors), func(index int) bool {
		return compareDiagnostic(c.errors[index], finding) > 0
	})
	if len(c.errors) == EditorGraphMaxDiagnostics {
		c.truncated = true
		if index == len(c.errors) {
			return
		}
		copy(c.errors[index+1:], c.errors[index:len(c.errors)-1])
		c.errors[index] = finding
		return
	}
	c.errors = append(c.errors, Diagnostic{})
	copy(c.errors[index+1:], c.errors[index:len(c.errors)-1])
	c.errors[index] = finding
}

func (c *Collector) Result() Result {
	result := BuildResult(c.errors, nil)
	result.Truncated = c.truncated
	return result
}
