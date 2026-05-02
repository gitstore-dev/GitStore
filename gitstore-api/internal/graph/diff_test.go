package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateDiff(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should detect no changes for identical content", func(t *testing.T) {
		content := "line 1\nline 2\nline 3"
		result := dg.GenerateDiff(content, content)

		assert.False(t, result.HasChanges)
		assert.Equal(t, content, result.OldContent)
		assert.Equal(t, content, result.NewContent)
	})

	t.Run("should detect additions", func(t *testing.T) {
		oldContent := "line 1\nline 2"
		newContent := "line 1\nline 2\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.True(t, result.HasChanges)
		assert.True(t, result.HasAdditions())
		assert.False(t, result.HasDeletions())
		assert.Contains(t, result.UnifiedDiff, "+ line 3")
	})

	t.Run("should detect deletions", func(t *testing.T) {
		oldContent := "line 1\nline 2\nline 3"
		newContent := "line 1\nline 2"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.True(t, result.HasChanges)
		assert.False(t, result.HasAdditions())
		assert.True(t, result.HasDeletions())
		assert.Contains(t, result.UnifiedDiff, "- line 3")
	})

	t.Run("should detect modifications", func(t *testing.T) {
		oldContent := "line 1\nold line\nline 3"
		newContent := "line 1\nnew line\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.True(t, result.HasChanges)
		assert.True(t, result.HasAdditions())
		assert.True(t, result.HasDeletions())
		// Character-level diff shows changes
		assert.Contains(t, result.UnifiedDiff, "- old")
		assert.Contains(t, result.UnifiedDiff, "+ new")
	})

	t.Run("should generate unified diff format", func(t *testing.T) {
		oldContent := "line 1\nline 2"
		newContent := "line 1\nmodified line 2"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.Contains(t, result.UnifiedDiff, "--- old version")
		assert.Contains(t, result.UnifiedDiff, "+++ new version")
		assert.Contains(t, result.UnifiedDiff, "  line 1")
	})
}

func TestExtractChanges(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should extract individual changes", func(t *testing.T) {
		oldContent := "line 1\nline 2\nline 3"
		newContent := "line 1\nmodified line 2\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.NotEmpty(t, result.Changes)

		// Should have changes detected
		hasEqual := false
		hasAdd := false

		for _, change := range result.Changes {
			switch change.Type {
			case ChangeTypeEqual:
				hasEqual = true
			case ChangeTypeAdd:
				hasAdd = true
			}
		}

		assert.True(t, hasEqual, "should have equal sections")
		assert.True(t, hasAdd, "should have additions")
	})

	t.Run("should include line numbers", func(t *testing.T) {
		oldContent := "line 1\nline 2"
		newContent := "line 1\nline 2\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)

		for _, change := range result.Changes {
			assert.Greater(t, change.LineNo, 0, "line number should be positive")
		}
	})
}

func TestGetChangedSections(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should return only changed sections", func(t *testing.T) {
		oldContent := "line 1\nline 2\nline 3"
		newContent := "line 1\nmodified line 2\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)
		changed := result.GetChangedSections()

		// Should only include deletions and additions, not equal parts
		for _, change := range changed {
			assert.NotEqual(t, ChangeTypeEqual, change.Type)
		}

		assert.Greater(t, len(changed), 0, "should have changes")
		assert.Less(t, len(changed), len(result.Changes), "should be fewer than total changes")
	})

	t.Run("should return empty for no changes", func(t *testing.T) {
		content := "line 1\nline 2"
		result := dg.GenerateDiff(content, content)

		changed := result.GetChangedSections()
		assert.Empty(t, changed)
	})
}

func TestGetDiffSummary(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should count added and deleted lines", func(t *testing.T) {
		oldContent := "line 1\nline 2\nline 3"
		newContent := "line 1\nmodified line 2\nline 3\nline 4"

		result := dg.GenerateDiff(oldContent, newContent)
		summary := result.GetDiffSummary()

		// Character-level diffs may show adds without deletes for some changes
		assert.Greater(t, summary.TotalChanges, 0)
		assert.Equal(t, summary.AddedLines+summary.DeletedLines, summary.ChangedLines)
		assert.Equal(t, summary.ChangedLines, summary.TotalChanges)
	})

	t.Run("should handle pure additions", func(t *testing.T) {
		oldContent := "line 1"
		newContent := "line 1\nline 2\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)
		summary := result.GetDiffSummary()

		assert.Greater(t, summary.AddedLines, 0)
		assert.Equal(t, 0, summary.DeletedLines)
	})

	t.Run("should handle pure deletions", func(t *testing.T) {
		oldContent := "line 1\nline 2\nline 3"
		newContent := "line 1"

		result := dg.GenerateDiff(oldContent, newContent)
		summary := result.GetDiffSummary()

		assert.Equal(t, 0, summary.AddedLines)
		assert.Greater(t, summary.DeletedLines, 0)
	})
}

func TestFormatDiffForDisplay(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should format with change counts", func(t *testing.T) {
		oldContent := "line 1\nline 2"
		newContent := "line 1\nline 2\nline 3"

		result := dg.GenerateDiff(oldContent, newContent)
		display := result.FormatDiffForDisplay()

		assert.Contains(t, display, "Changes:")
		assert.Contains(t, display, "+")
		assert.Contains(t, display, "---")
		assert.Contains(t, display, "+++")
	})

	t.Run("should show no changes message", func(t *testing.T) {
		content := "line 1\nline 2"
		result := dg.GenerateDiff(content, content)
		display := result.FormatDiffForDisplay()

		assert.Equal(t, "No changes detected", display)
	})
}

func TestGenerateConflictDiff(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should generate three-way diff", func(t *testing.T) {
		baseContent := "line 1\nline 2\nline 3"
		localContent := "line 1\nlocal change\nline 3"
		remoteContent := "line 1\nremote change\nline 3"

		conflict := dg.GenerateConflictDiff(baseContent, localContent, remoteContent)

		assert.NotNil(t, conflict.LocalDiff)
		assert.NotNil(t, conflict.RemoteDiff)
		assert.Equal(t, baseContent, conflict.BaseContent)
		assert.Equal(t, localContent, conflict.LocalContent)
		assert.Equal(t, remoteContent, conflict.RemoteContent)
		assert.True(t, conflict.HasConflict)
	})

	t.Run("should detect no conflict when only local changes", func(t *testing.T) {
		baseContent := "line 1\nline 2"
		localContent := "line 1\nlocal change"
		remoteContent := "line 1\nline 2" // Same as base

		conflict := dg.GenerateConflictDiff(baseContent, localContent, remoteContent)

		assert.True(t, conflict.LocalDiff.HasChanges)
		assert.False(t, conflict.RemoteDiff.HasChanges)
		assert.False(t, conflict.HasConflict)
	})

	t.Run("should detect no conflict when only remote changes", func(t *testing.T) {
		baseContent := "line 1\nline 2"
		localContent := "line 1\nline 2" // Same as base
		remoteContent := "line 1\nremote change"

		conflict := dg.GenerateConflictDiff(baseContent, localContent, remoteContent)

		assert.False(t, conflict.LocalDiff.HasChanges)
		assert.True(t, conflict.RemoteDiff.HasChanges)
		assert.False(t, conflict.HasConflict)
	})

	t.Run("should detect conflict when both sides have changes", func(t *testing.T) {
		baseContent := "line 1\nline 2\nline 3"
		localContent := "line 1\nlocal line 2\nline 3"
		remoteContent := "line 1\nremote line 2\nline 3"

		conflict := dg.GenerateConflictDiff(baseContent, localContent, remoteContent)

		assert.True(t, conflict.LocalDiff.HasChanges)
		assert.True(t, conflict.RemoteDiff.HasChanges)
		assert.True(t, conflict.HasConflict)
	})
}

func TestGetConflictSummary(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should generate readable conflict summary", func(t *testing.T) {
		baseContent := "line 1\nline 2"
		localContent := "line 1\nlocal change"
		remoteContent := "line 1\nremote change"

		conflict := dg.GenerateConflictDiff(baseContent, localContent, remoteContent)
		summary := conflict.GetConflictSummary()

		assert.Contains(t, summary, "Conflict detected")
		assert.Contains(t, summary, "Your changes")
		assert.Contains(t, summary, "Server changes")
	})

	t.Run("should show no conflict message", func(t *testing.T) {
		baseContent := "line 1\nline 2"
		localContent := "line 1\nline 2"
		remoteContent := "line 1\nline 2"

		conflict := dg.GenerateConflictDiff(baseContent, localContent, remoteContent)
		summary := conflict.GetConflictSummary()

		assert.Equal(t, "No conflict detected", summary)
	})
}

func TestHasAdditionsAndDeletions(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should detect additions only", func(t *testing.T) {
		oldContent := "line 1"
		newContent := "line 1\nline 2"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.True(t, result.HasAdditions())
		assert.False(t, result.HasDeletions())
	})

	t.Run("should detect deletions only", func(t *testing.T) {
		oldContent := "line 1\nline 2"
		newContent := "line 1"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.False(t, result.HasAdditions())
		assert.True(t, result.HasDeletions())
	})

	t.Run("should detect both additions and deletions", func(t *testing.T) {
		oldContent := "line 1\nold line"
		newContent := "line 1\nnew line\nadded line"

		result := dg.GenerateDiff(oldContent, newContent)

		assert.True(t, result.HasAdditions())
		assert.True(t, result.HasDeletions())
	})

	t.Run("should detect neither for no changes", func(t *testing.T) {
		content := "line 1\nline 2"

		result := dg.GenerateDiff(content, content)

		assert.False(t, result.HasAdditions())
		assert.False(t, result.HasDeletions())
	})
}

func TestRealWorldScenarios(t *testing.T) {
	dg := NewDiffGenerator()

	t.Run("should handle product price update", func(t *testing.T) {
		oldProduct := `---
title: Premium Laptop
price: 999.99
---

# Premium Laptop

A great laptop for professionals.`

		newProduct := `---
title: Premium Laptop
price: 1299.99
---

# Premium Laptop

A great laptop for professionals.`

		result := dg.GenerateDiff(oldProduct, newProduct)

		assert.True(t, result.HasChanges)
		// Character-level diff shows the numeric change
		assert.Contains(t, result.UnifiedDiff, "price:")
		assert.Contains(t, result.UnifiedDiff, "- 9")
		assert.Contains(t, result.UnifiedDiff, "+ 12")
	})

	t.Run("should handle product description update", func(t *testing.T) {
		oldProduct := `# Laptop

Basic description.`

		newProduct := `# Laptop

Enhanced description with more details.`

		result := dg.GenerateDiff(oldProduct, newProduct)

		assert.True(t, result.HasChanges)
		summary := result.GetDiffSummary()
		assert.Greater(t, summary.TotalChanges, 0)
	})

	t.Run("should handle concurrent edit conflict", func(t *testing.T) {
		baseProduct := `---
title: Product
price: 100.00
inventory: 50
---`

		userEdit := `---
title: Product (Updated)
price: 100.00
inventory: 50
---`

		serverEdit := `---
title: Product
price: 95.00
inventory: 45
---`

		conflict := dg.GenerateConflictDiff(baseProduct, userEdit, serverEdit)

		assert.True(t, conflict.HasConflict)
		summary := conflict.GetConflictSummary()
		assert.Contains(t, summary, "Conflict detected")
		assert.Contains(t, summary, "Your changes")
		assert.Contains(t, summary, "Server changes")
	})
}
