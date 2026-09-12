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
