# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

package gitstore.rbac

decision := {"allow": true, "masters": true, "binding": "", "role": "", "rule": -1} if {
	input.request.masters
} else := {"allow": true, "masters": false, "binding": match[0], "role": match[1], "rule": match[2]} if {
	match := sort(matches)[0]
} else := {"allow": false, "masters": false, "binding": "", "role": "", "rule": -1}

matches contains [binding.uid, role.uid, rule.index] if {
	some tier in input.tiers
	some subject in input.request.subjects
	some binding_id
	tier.subjects[subject][binding_id]
	binding := tier.bindings[binding_id]
	binding_scope(binding)
	some role_tier in input.tiers
	role := role_tier.roles[binding.role_uid]
	role.kind == binding.role_kind
	role.name == binding.role_name
	role_scope(binding, role)
	some rule in role.actions[input.request.action]
	instance_matches(rule)
	every condition in rule.when {
		condition_matches(condition)
	}
	transfer_matches(binding, rule)
}

binding_scope(binding) if {
	binding.cluster
}

binding_scope(binding) if {
	not binding.cluster
	input.request.scope == "namespace"
	input.request.namespace != ""
	binding.namespace == input.request.namespace
}

role_scope(binding, role) if {
	role.kind == "ClusterRole"
	role.namespace_uid == ""
}

role_scope(binding, role) if {
	role.kind == "Role"
	not binding.cluster
	role.namespace_uid == binding.namespace_uid
}

instance_matches(rule) if {
	count(rule.names) == 0
}

instance_matches(rule) if {
	input.request.name != ""
	input.request.name in rule.names
}

member(value) if {
	value != ""
	value == input.request.subject
}

member(value) if {
	value != ""
	value in input.request.groups
}

condition_matches(condition) if {
	condition.token == "owner"
	member(input.request.owner)
}

condition_matches(condition) if {
	condition.token == "targetOwnerMember"
	member(input.request.target)
}

condition_matches(condition) if {
	condition.token == "subjectUsername"
	input.request.username != ""
	input.request.username == input.request.name
}

condition_matches(condition) if {
	condition.token == ""
	actual := object.get(input.request.attrs, condition.attr, null)
	is_string(actual)
	count(actual) <= 256
	comparison(condition, actual)
}

comparison(condition, actual) if {
	condition.op == "eq"
	actual == condition.value
}

comparison(condition, actual) if {
	condition.op == "neq"
	actual != condition.value
}

comparison(condition, actual) if {
	condition.op == "in"
	actual in condition.values
}

comparison(condition, actual) if {
	condition.op == "notIn"
	not actual in condition.values
}

transfer_matches(binding, rule) if {
	input.request.verb != "transfer"
}

transfer_matches(binding, rule) if {
	input.request.verb == "transfer"
	member(input.request.owner)
	member(input.request.target)
}

transfer_matches(binding, rule) if {
	input.request.verb == "transfer"
	member(input.request.owner)
	count(rule.when) == 0
}

transfer_matches(binding, rule) if {
	input.request.verb == "transfer"
	binding.cluster
	count(rule.when) == 0
}

coverage := {"covered": true} if {
	input.request.masters
} else := {"covered": true} if {
	every permission in input.request.permissions {
		permission_covered(permission)
	}
} else := {"covered": false}

coverage_rules(action) := {rule |
	some tier in input.tiers
	some subject in input.request.subjects
	some binding_id
	tier.subjects[subject][binding_id]
	binding := tier.bindings[binding_id]
	binding_scope(binding)
	some role_tier in input.tiers
	role := role_tier.roles[binding.role_uid]
	role.kind == binding.role_kind
	role.name == binding.role_name
	role_scope(binding, role)
	some rule in role.actions[action]
	count(rule.when) == 0
}

permission_covered(permission) if {
	count(permission.names) == 0
	some rule in coverage_rules(permission.action)
	count(rule.names) == 0
}

permission_covered(permission) if {
	count(permission.names) != 0
	rules := coverage_rules(permission.action)
	every name in permission.names {
		name_covered(rules, name)
	}
}

name_covered(rules, name) if {
	some rule in rules
	count(rule.names) == 0
}

name_covered(rules, name) if {
	some rule in rules
	name in rule.names
}
