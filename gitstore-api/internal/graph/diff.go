package graph

import (
	"fmt"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// DiffGenerator generates diffs between file versions for conflict resolution
type DiffGenerator struct {
	dmp *diffmatchpatch.DiffMatchPatch
}

// NewDiffGenerator creates a new diff generator
func NewDiffGenerator() *DiffGenerator {
	return &DiffGenerator{
		dmp: diffmatchpatch.New(),
	}
}

// DiffResult contains the diff between two versions
type DiffResult struct {
	OldContent  string
	NewContent  string
	UnifiedDiff string
	Changes     []Change
	HasChanges  bool
}

// Change represents a single change in the diff
type Change struct {
	Type    ChangeType
	Content string
	LineNo  int
}

// ChangeType indicates the type of change
type ChangeType string

const (
	ChangeTypeAdd    ChangeType = "add"
	ChangeTypeDelete ChangeType = "delete"
	ChangeTypeEqual  ChangeType = "equal"
)

// GenerateDiff creates a diff between two content versions
func (dg *DiffGenerator) GenerateDiff(oldContent, newContent string) *DiffResult {
	// Calculate diffs
	diffs := dg.dmp.DiffMain(oldContent, newContent, false)

	// Clean up the diffs for readability
	diffs = dg.dmp.DiffCleanupSemantic(diffs)

	// Generate unified diff format
	unifiedDiff := dg.generateUnifiedDiff(oldContent, newContent, diffs)

	// Extract changes
	changes := dg.extractChanges(diffs)

	return &DiffResult{
		OldContent:  oldContent,
		NewContent:  newContent,
		UnifiedDiff: unifiedDiff,
		Changes:     changes,
		HasChanges:  len(diffs) > 0 && !dg.isOnlyEqual(diffs),
	}
}

// generateUnifiedDiff creates a unified diff format string
func (dg *DiffGenerator) generateUnifiedDiff(oldContent, newContent string, diffs []diffmatchpatch.Diff) string {
	var result strings.Builder

	result.WriteString("--- old version\n")
	result.WriteString("+++ new version\n")

	// Simple diff format showing additions and deletions
	for _, diff := range diffs {
		lines := strings.Split(diff.Text, "\n")

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			for i, line := range lines {
				if i < len(lines)-1 || line != "" {
					result.WriteString(fmt.Sprintf("  %s\n", line))
				}
			}
		case diffmatchpatch.DiffDelete:
			for i, line := range lines {
				if i < len(lines)-1 || line != "" {
					result.WriteString(fmt.Sprintf("- %s\n", line))
				}
			}
		case diffmatchpatch.DiffInsert:
			for i, line := range lines {
				if i < len(lines)-1 || line != "" {
					result.WriteString(fmt.Sprintf("+ %s\n", line))
				}
			}
		}
	}

	return result.String()
}

// extractChanges converts diffs into a structured list of changes
func (dg *DiffGenerator) extractChanges(diffs []diffmatchpatch.Diff) []Change {
	var changes []Change
	lineNo := 1

	for _, diff := range diffs {
		var changeType ChangeType
		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			changeType = ChangeTypeEqual
		case diffmatchpatch.DiffDelete:
			changeType = ChangeTypeDelete
		case diffmatchpatch.DiffInsert:
			changeType = ChangeTypeAdd
		}

		// Split by lines for proper line-based diff
		lines := strings.Split(diff.Text, "\n")
		for i, line := range lines {
			// Skip the last empty line from split
			if i == len(lines)-1 && line == "" {
				continue
			}

			changes = append(changes, Change{
				Type:    changeType,
				Content: line,
				LineNo:  lineNo,
			})

			if diff.Type == diffmatchpatch.DiffEqual || diff.Type == diffmatchpatch.DiffInsert {
				lineNo++
			}
		}
	}

	return changes
}

// isOnlyEqual checks if diffs contain only equal sections (no changes)
func (dg *DiffGenerator) isOnlyEqual(diffs []diffmatchpatch.Diff) bool {
	for _, diff := range diffs {
		if diff.Type != diffmatchpatch.DiffEqual {
			return false
		}
	}
	return true
}

// GenerateConflictDiff creates a three-way diff for conflict resolution
// Shows the common base, user's changes, and server's changes
func (dg *DiffGenerator) GenerateConflictDiff(baseContent, localContent, remoteContent string) *ConflictDiff {
	localDiff := dg.GenerateDiff(baseContent, localContent)
	remoteDiff := dg.GenerateDiff(baseContent, remoteContent)

	return &ConflictDiff{
		BaseContent:   baseContent,
		LocalContent:  localContent,
		RemoteContent: remoteContent,
		LocalDiff:     localDiff,
		RemoteDiff:    remoteDiff,
		HasConflict:   localDiff.HasChanges && remoteDiff.HasChanges,
	}
}

// ConflictDiff represents a three-way merge conflict
type ConflictDiff struct {
	BaseContent   string
	LocalContent  string
	RemoteContent string
	LocalDiff     *DiffResult
	RemoteDiff    *DiffResult
	HasConflict   bool
}

// GetConflictSummary generates a human-readable summary of the conflict
func (cd *ConflictDiff) GetConflictSummary() string {
	if !cd.HasConflict {
		return "No conflict detected"
	}

	var summary strings.Builder
	summary.WriteString("Conflict detected:\n\n")

	summary.WriteString("Your changes:\n")
	summary.WriteString(cd.LocalDiff.UnifiedDiff)
	summary.WriteString("\n")

	summary.WriteString("Server changes:\n")
	summary.WriteString(cd.RemoteDiff.UnifiedDiff)

	return summary.String()
}

// DiffSummary provides a brief summary of changes
type DiffSummary struct {
	AddedLines   int
	DeletedLines int
	ChangedLines int
	TotalChanges int
}

// GetDiffSummary calculates statistics about the diff
func (dr *DiffResult) GetDiffSummary() *DiffSummary {
	summary := &DiffSummary{}

	for _, change := range dr.Changes {
		switch change.Type {
		case ChangeTypeAdd:
			summary.AddedLines++
		case ChangeTypeDelete:
			summary.DeletedLines++
		}
	}

	summary.ChangedLines = summary.AddedLines + summary.DeletedLines
	summary.TotalChanges = summary.ChangedLines

	return summary
}

// FormatDiffForDisplay formats the diff for display in UI
func (dr *DiffResult) FormatDiffForDisplay() string {
	if !dr.HasChanges {
		return "No changes detected"
	}

	var output strings.Builder

	summary := dr.GetDiffSummary()
	output.WriteString(fmt.Sprintf("Changes: +%d -%d\n\n", summary.AddedLines, summary.DeletedLines))
	output.WriteString(dr.UnifiedDiff)

	return output.String()
}

// GetChangedSections returns only the sections with changes (ignoring equal parts)
func (dr *DiffResult) GetChangedSections() []Change {
	var changed []Change
	for _, change := range dr.Changes {
		if change.Type != ChangeTypeEqual {
			changed = append(changed, change)
		}
	}
	return changed
}

// HasAdditions checks if the diff contains any additions
func (dr *DiffResult) HasAdditions() bool {
	for _, change := range dr.Changes {
		if change.Type == ChangeTypeAdd {
			return true
		}
	}
	return false
}

// HasDeletions checks if the diff contains any deletions
func (dr *DiffResult) HasDeletions() bool {
	for _, change := range dr.Changes {
		if change.Type == ChangeTypeDelete {
			return true
		}
	}
	return false
}
