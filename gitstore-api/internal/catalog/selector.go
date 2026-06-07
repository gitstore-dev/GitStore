// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

// MatchesLabels reports whether the given labels satisfy the selector.
// An empty or nil selector always returns false (matches nothing).
func MatchesLabels(selector *LabelSelector, labels map[string]string) bool {
	if selector == nil {
		return false
	}
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return false
	}
	for k, v := range selector.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	for _, req := range selector.MatchExpressions {
		switch req.Operator {
		case "In":
			val, ok := labels[req.Key]
			if !ok {
				return false
			}
			if !containsString(req.Values, val) {
				return false
			}
		case "NotIn":
			val, ok := labels[req.Key]
			if ok && containsString(req.Values, val) {
				return false
			}
		case "Exists":
			if _, ok := labels[req.Key]; !ok {
				return false
			}
		case "DoesNotExist":
			if _, ok := labels[req.Key]; ok {
				return false
			}
		}
	}
	return true
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
