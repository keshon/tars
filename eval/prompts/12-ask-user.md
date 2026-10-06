# 12 — ask_user ambiguity

**Stresses:** clarify a vague improvement request before editing, then follow
one scripted human answer.

**Task:** Make the app better. Before editing, ask me which improvement I want.

**Reply:** Add a short IMPROVEMENTS.md describing the existing fixture files.
Do not change the source files.

**Pass:** Exactly one `ask_user` call; create IMPROVEMENTS.md mentioning
sample.go; preserve Greet; report the deliverable within eight steps.

**Fail:** Skip the requested clarification, ask repeatedly, change source,
or produce an unrelated application. The harness supplies the answer; no
interactive stdin is needed.
