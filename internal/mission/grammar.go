package mission

// GBNF grammars for the mission's structured model calls. Both are sent
// as per-request grammars on tool-free Chat calls — the risky
// grammar-plus-tools combination koboldcpp's docs are vague about never
// occurs (see ARCHITECTURE.md "Grammar as a lever").
//
// A grammar guarantees syntax only: the model cannot emit malformed JSON,
// but it can still emit a syntactically perfect plan with vacuous goals
// or an `echo ok` check. Structure is the Go validator's job (plan.go);
// semantics are the human approval gate's. Three cheap layers, each
// catching what the previous one can't.
//
// Grammar notes:
//   - {m,n} repetition bounds are llama.cpp GBNF extensions; current
//     koboldcpp bundles a grammar engine that supports them, but this is
//     backend behavior, not Go-testable — confirm the first live run
//     with -debug before trusting it, and fall back to '*' plus Go-side
//     length/count validation if the backend rejects the grammar.
//   - Rule names must be [a-zA-Z0-9]+ with NO underscores. koboldcpp's
//     GBNF parser treats "_" as ending a name (`path_contains` → `path`
//     then orphan `_contains`), then Ignored invalid grammar sampler —
//     live failure 2026-07-14 left planning unconstrained.
//   - String contents follow llama.cpp's own json.gbnf character class,
//     so any JSON string the grammar admits is decodable by encoding/json.
//   - Key order inside objects is FIXED. A weak model given key freedom
//     spends tokens deciding; forced order turns each key into a
//     zero-entropy continuation.

// PlanGrammar constrains a planning response to a flat subtask list —
// at most 8 subtasks, each with bounded string lengths, at most 5
// acceptance criteria / file hints, and a typed check.
// acceptance uses accarr (at least one string, grammar-enforced): a live
// Qwen replan emitted `"acceptance": []` twice in a row and burned both
// generation attempts on the same validator rejection — making the empty
// array unrepresentable is cheaper than explaining it.
const PlanGrammar = `root ::= "{" ws "\"subtasks\"" ws ":" ws "[" ws subtask (ws "," ws subtask){0,7} ws "]" ws "}"
subtask ::= "{" ws "\"id\"" ws ":" ws str "," ws "\"milestone\"" ws ":" ws str "," ws "\"title\"" ws ":" ws str "," ws "\"goal\"" ws ":" ws str "," ws "\"acceptance\"" ws ":" ws accarr "," ws "\"files_hint\"" ws ":" ws strarr "," ws "\"check\"" ws ":" ws check ws "}"
check ::= "{" ws "\"type\"" ws ":" ws checktype (ws "," ws checkfields)? ws "}"
checktype ::= "\"shell\"" | "\"file_exists\"" | "\"file_absent\"" | "\"content_contains\"" | "\"http\"" | "\"none\""
checkfields ::= pathcont | onearg
pathcont ::= "\"path\"" ws ":" ws str ws "," ws "\"contains\"" ws ":" ws str
onearg ::= ("\"cmd\"" | "\"path\"" | "\"url\"") ws ":" ws str
accarr ::= "[" ws str (ws "," ws str){0,4} ws "]"
strarr ::= "[" ws (str (ws "," ws str){0,4})? ws "]"
str ::= "\"" schar{1,300} "\""
schar ::= [^"\\\x7F\x00-\x1F] | "\\" (["\\bfnrt/] | "u" [0-9a-fA-F]{4})
ws ::= [ \t\n]{0,4}
`

// DecisionGrammar constrains a verdict response ("is this done?" /
// "does the result have gaps?") to a two-way choice plus a bounded
// note — the narrowest decision a weak model can be asked to make.
const DecisionGrammar = `root ::= "{" ws "\"verdict\"" ws ":" ws ("\"ok\"" | "\"gaps\"") ws "," ws "\"notes\"" ws ":" ws "\"" schar{0,300} "\"" ws "}"
schar ::= [^"\\\x7F\x00-\x1F] | "\\" (["\\bfnrt/] | "u" [0-9a-fA-F]{4})
ws ::= [ \t\n]{0,4}
`

// PlanSchema is PlanGrammar expressed as JSON Schema, for backends that
// constrain structured output that way rather than with GBNF.
//
// llama-server accepts `grammar` only on /completion; on
// /v1/chat/completions it wants response_format, and silently ignores a
// grammar field it does not understand. That silence cost a full mission
// sweep: every plan call ran unconstrained, and the model degenerated
// mid-JSON into a repeated token, leaving "unexpected end of JSON input"
// in nine runs out of nine.
//
// Kept beside the grammar deliberately. Two encodings of one contract
// will drift unless they are read together, and TestPlanSchemaMatches
// GrammarBounds checks the bounds they share.
const PlanSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["subtasks"],
  "properties": {
    "subtasks": {
      "type": "array", "minItems": 1, "maxItems": 8,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id","milestone","title","goal","acceptance","files_hint","check"],
        "properties": {
          "id":         {"type": "string", "maxLength": 300},
          "milestone":  {"type": "string", "maxLength": 300},
          "title":      {"type": "string", "maxLength": 300},
          "goal":       {"type": "string", "maxLength": 300},
          "acceptance": {"type": "array", "minItems": 1, "maxItems": 5,
                         "items": {"type": "string", "maxLength": 300}},
          "files_hint": {"type": "array", "maxItems": 5,
                         "items": {"type": "string", "maxLength": 300}},
          "check": {
            "type": "object",
            "required": ["type"],
            "properties": {
              "type":     {"enum": ["shell","file_exists","file_absent","content_contains","http","none"]},
              "cmd":      {"type": "string", "maxLength": 300},
              "path":     {"type": "string", "maxLength": 300},
              "contains": {"type": "string", "maxLength": 300},
              "url":      {"type": "string", "maxLength": 300}
            }
          }
        }
      }
    }
  }
}`

// DecisionSchema is DecisionGrammar as JSON Schema. See PlanSchema for
// why both encodings exist.
const DecisionSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["verdict", "notes"],
  "properties": {
    "verdict": {"enum": ["ok", "gaps"]},
    "notes":   {"type": "string", "maxLength": 300}
  }
}`
