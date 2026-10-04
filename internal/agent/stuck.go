package agent

import (
	"hash/fnv"

	"github.com/keshon/tars/internal/llm"
)

// trackSingleToolLoop counts consecutive steps where the model issued
// exactly one tool call, it's the same tool name as the previous such
// step, and it returned the same bytes. Novelty restarts the chain:
// read a/b/c with fresh results never accumulates, while grinding one
// query with slightly different arguments — the case exact-repeat
// detection misses — still trips at maxSameToolSteps. Errors fold in
// as their message, so three identical failures count as one grind.
// maxSameToolSteps is how many consecutive non-mutating steps calling one
// tool alone trip the tool-loop nudge.
const maxSameToolSteps = 3

func (a *Agent) trackSingleToolLoop(calls []llm.ToolCall, results []callResult, st *runState) {
	if len(calls) != 1 {
		st.lastSingleTool = ""
		st.lastSingleResult = 0
		st.consecutiveSameToolCount = 0
		return
	}
	// results parallels calls by construction (one entry per issued
	// call); the length check is a guard against indexing, not a case
	// the loop produces.
	var h uint64
	if len(results) > 0 {
		h = resultHash(results[0])
	}
	name := calls[0].Name
	if name == st.lastSingleTool && h == st.lastSingleResult {
		st.consecutiveSameToolCount++
		return
	}
	st.lastSingleTool = name
	st.lastSingleResult = h
	st.consecutiveSameToolCount = 1
}

// resultHash fingerprints one call's outcome for the same-tool loop
// counter. FNV-64a, not crypto: a collision costs one advisory nudge,
// the fail-safe direction — identical to today's behavior.
func resultHash(r callResult) uint64 {
	h := fnv.New64a()
	if r.err != nil {
		h.Write([]byte("error: " + r.err.Error()))
	} else {
		h.Write([]byte(r.content))
	}
	return h.Sum64()
}
