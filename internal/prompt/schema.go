package prompt

// ReviewerSchema is the JSON Schema of an agent report, also usable with
// tools that accept a schema for structured output (claude --json-schema).
const ReviewerSchema = `{
  "type": "object",
  "required": ["schema_version", "reviewer", "summary", "findings", "verdict"],
  "properties": {
    "schema_version": {"type": "string", "const": "1"},
    "reviewer": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}, "model": {"type": "string"}}},
    "summary": {"type": "string"},
    "verdict": {"type": "string", "enum": ["approve", "comment", "request_changes"]},
    "findings": {"type": "array", "items": {
      "type": "object",
      "required": ["id", "category", "severity", "confidence", "file", "start_line", "end_line", "title", "description"],
      "properties": {
        "id": {"type": "string"},
        "category": {"type": "string"},
        "severity": {"type": "string", "enum": ["critical", "high", "medium", "low", "info"]},
        "confidence": {"type": "number", "minimum": 0, "maximum": 1},
        "file": {"type": "string"},
        "start_line": {"type": "integer", "minimum": 1},
        "end_line": {"type": "integer", "minimum": 1},
        "title": {"type": "string"},
        "description": {"type": "string"},
        "evidence": {"type": "string"},
        "suggestion": {"type": "string"}
      }}},
    "questions": {"type": "array", "items": {
      "type": "object", "required": ["question"],
      "properties": {"file": {"type": "string"}, "line": {"type": "integer"}, "question": {"type": "string"}}}}
  }
}`

// LeadSchema is the JSON Schema of the consolidated review the lead must emit.
const LeadSchema = `{
  "type": "object",
  "required": ["schema_version", "summary", "verdict", "findings", "failed_reviewers"],
  "properties": {
    "schema_version": {"type": "string", "const": "1"},
    "summary": {"type": "string"},
    "verdict": {"type": "string", "enum": ["approve", "comment", "request_changes"]},
    "findings": {"type": "array", "items": {
      "type": "object",
      "required": ["category", "severity", "confidence", "file", "start_line", "end_line", "title", "description", "reported_by"],
      "properties": {
        "category": {"type": "string"},
        "severity": {"type": "string", "enum": ["critical", "high", "medium", "low", "info"]},
        "confidence": {"type": "number", "minimum": 0, "maximum": 1},
        "file": {"type": "string"},
        "start_line": {"type": "integer", "minimum": 1},
        "end_line": {"type": "integer", "minimum": 1},
        "title": {"type": "string"},
        "description": {"type": "string"},
        "evidence": {"type": "string"},
        "suggestion": {"type": "string"},
        "reported_by": {"type": "array", "items": {"type": "string"}},
        "out_of_scope": {"type": "boolean"}
      }}},
    "questions": {"type": "array", "items": {
      "type": "object", "required": ["question"],
      "properties": {"file": {"type": "string"}, "line": {"type": "integer"}, "question": {"type": "string"}}}},
    "failed_reviewers": {"type": "array", "items": {
      "type": "object", "required": ["id", "reason"],
      "properties": {"id": {"type": "string"}, "reason": {"type": "string"}}}}
  }
}`

// TriageSchema is the JSON Schema of a triage report for one issue.
const TriageSchema = `{
  "type": "object",
  "required": ["schema_version", "reviewer", "number", "labels", "status", "confidence", "summary", "evidence"],
  "properties": {
    "schema_version": {"type": "string", "const": "1"},
    "reviewer": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}, "model": {"type": "string"}}},
    "number": {"type": "integer"},
    "labels": {"type": "array", "items": {"type": "string"}},
    "status": {"type": "string", "enum": ["still-present", "fixed", "obsolete", "duplicate-of", "needs-info"]},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1},
    "summary": {"type": "string"},
    "evidence": {"type": "string"},
    "duplicate_of": {"type": "integer"},
    "question": {"type": "string"}
  }
}`

// LeadTriageSchema is the JSON Schema of the consolidated triage.
const LeadTriageSchema = `{
  "type": "object",
  "required": ["schema_version", "summary", "issues"],
  "properties": {
    "schema_version": {"type": "string", "const": "1"},
    "summary": {"type": "string"},
    "issues": {"type": "array", "items": {
      "type": "object",
      "required": ["number", "labels", "status", "confidence", "summary", "evidence", "reported_by"],
      "properties": {
        "number": {"type": "integer"},
        "labels": {"type": "array", "items": {"type": "string"}},
        "status": {"type": "string", "enum": ["still-present", "fixed", "obsolete", "duplicate-of", "needs-info"]},
        "confidence": {"type": "number", "minimum": 0, "maximum": 1},
        "summary": {"type": "string"},
        "evidence": {"type": "string"},
        "duplicate_of": {"type": "integer"},
        "question": {"type": "string"},
        "reported_by": {"type": "array", "items": {"type": "string"}}
      }}}
  }
}`

// PlanSchema is the JSON Schema of an implementation plan for one issue.
const PlanSchema = `{
  "type": "object",
  "required": ["schema_version", "reviewer", "number", "understanding", "approach", "steps", "effort", "confidence"],
  "properties": {
    "schema_version": {"type": "string", "const": "1"},
    "reviewer": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}, "model": {"type": "string"}}},
    "number": {"type": "integer"},
    "understanding": {"type": "string"},
    "approach": {"type": "string"},
    "alternatives": {"type": "array", "items": {
      "type": "object", "required": ["approach", "why_not"],
      "properties": {"approach": {"type": "string"}, "why_not": {"type": "string"}}}},
    "steps": {"type": "array", "items": {
      "type": "object",
      "required": ["id", "title", "details"],
      "properties": {
        "id": {"type": "string"},
        "title": {"type": "string"},
        "details": {"type": "string"},
        "files": {"type": "array", "items": {"type": "string"}},
        "validation": {"type": "string"},
        "depends_on": {"type": "array", "items": {"type": "string"}}
      }}},
    "tests": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {
      "type": "object", "required": ["description"],
      "properties": {"description": {"type": "string"}, "mitigation": {"type": "string"}}}},
    "open_questions": {"type": "array", "items": {"type": "string"}},
    "effort": {"type": "string", "enum": ["small", "medium", "large"]},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1}
  }
}`

// LeadPlanSchema is the JSON Schema of the consolidated plan.
const LeadPlanSchema = `{
  "type": "object",
  "required": ["schema_version", "number", "summary", "approach", "steps", "effort", "confidence", "reported_by"],
  "properties": {
    "schema_version": {"type": "string", "const": "1"},
    "number": {"type": "integer"},
    "summary": {"type": "string"},
    "understanding": {"type": "string"},
    "approach": {"type": "string"},
    "alternatives": {"type": "array", "items": {
      "type": "object", "required": ["approach", "why_not"],
      "properties": {"approach": {"type": "string"}, "why_not": {"type": "string"}}}},
    "steps": {"type": "array", "items": {
      "type": "object",
      "required": ["id", "title", "details"],
      "properties": {
        "id": {"type": "string"},
        "title": {"type": "string"},
        "details": {"type": "string"},
        "files": {"type": "array", "items": {"type": "string"}},
        "validation": {"type": "string"},
        "depends_on": {"type": "array", "items": {"type": "string"}}
      }}},
    "tests": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {
      "type": "object", "required": ["description"],
      "properties": {"description": {"type": "string"}, "mitigation": {"type": "string"}}}},
    "open_questions": {"type": "array", "items": {"type": "string"}},
    "effort": {"type": "string", "enum": ["small", "medium", "large"]},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1},
    "reported_by": {"type": "array", "items": {"type": "string"}}
  }
}`
