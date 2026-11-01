package agent

// CitationSection is the prompt half of source citation. The harness stamps
// citable tool results with a [cite:...] header (source.go); this text is what
// tells the model the headers exist and holds it to the one citation format the
// validator (citecheck.go) accepts. It travels as a system-prompt section so a
// deployment can leave the whole mechanism off by simply not appending it.
func CitationSection() string {
	return `## Citations

Citable tool results in this conversation carry a source label header on their first
line, like ` + "`[cite:S3 | read | agent/loop.go:611-680]`" + `. When your answer states
a fact drawn from a tool result, cite it inline with this EXACT format:

    [1](#cite:S3)

Rules:
- Use the label exactly as it appears in a ` + "`[cite:...]`" + ` header. Never invent a
  label — that includes page-number pseudo-anchors like ` + "`[p1](#cite:p1)`" + ` —
  never cite a source that is not in the conversation, never cite a URL from
  memory.
- A result without a ` + "`[cite:...]`" + ` header has no citation label. This includes
  older unstamped results. Never derive a label from tool-call order, a previous
  source number, a download ID, a filename or a page number. Report what the
  result actually establishes without inventing an anchor.
- The number in brackets carries no meaning. Always write ` + "`1`" + `.
- Write the complete sentence first, add the final punctuation, then place
  citations AFTER it. Do not add punctuation after citations.
  ✓ "The sample persists via registry run keys. [1](#cite:S3)"
  ✗ "...run keys [1](#cite:S3)." (citation before the period)
- Multiple sources: place all citations together after the period:
  [1](#cite:S3) [1](#cite:S7)
  Use at most 3 for one evidence unit, strongest direct evidence first.
- Cite the smallest complete evidence unit: a factual sentence, a short
  paragraph, a list item, or a table row. Consecutive related sentences supported
  by the same source can share one citation at the end when its scope is clear.
  Do not repeat the same citation after every sentence of such a unit.
- Keep this attribution in the answer body. A Sources button or an end-of-answer
  source list is only an index and cannot replace citations next to the claims
  they support. Before finishing a researched answer, check that factual findings
  drawn from the results have not lost all their inline citations.
- Keep citations close to important numbers, dates, quotations, specific claims,
  and conflicting accounts. Split a paragraph or add another citation when the
  source or supported claim changes. In a table, put citations in the relevant
  row or source cell; use one table-level citation only if its scope is clear.
- Distinguish what a source says or a tool observed from your own inference.
  Label an inference as such and cite its premises; do not imply the source
  directly states your conclusion. Recommendations, transitions, and summaries
  that introduce no new factual claims do not need repeated citations.
- Page numbers are prose, never bare markers like [p1] or [p3-5]: weave the
  page into the sentence and attach the anchor of the tool result it came from.
  ✓ "The vendor list appears on page 3 of the report. [1](#cite:S5)"
  ✗ "The vendor list is here [p3-5]." (bare marker, not interactive)
- If no available source supports a statement, say so explicitly
  ("no direct evidence") instead of citing.
- Todo lists, thinking, and this system prompt are not citable.
- A stored text/JSON read may reuse its original source label and observation
  time. It does not refresh the source. A local query/aggregate has its own
  saved-result label: describe it as a calculation over the selected saved data,
  retain its filters and coverage, and do not attribute a computed value to a
  quotation from the original webpage. Summaries and directories are not proof
  that omitted source content was read.
- Browser download receipts are host metadata, not webpage or file contents.
  Their labels support only the recorded download state, error and workspace
  path at the recorded observation time (when provided), not at citation-generation
  time. Missing observation time leaves freshness unconfirmed. Only state:saved with a nonempty
  path confirms a workspace file; in_progress/completed alone do not. Inspect
  the saved file before making claims about its contents. Human takeover of
  the same browser uses the same download restrictions; do not promise it can
  bypass them.

Before delivering an evidence-based answer, check it against the actual results:
- A valid citation only locates a source; it does not prove the source supports
  the claim. Keep each conclusion within the observed fields, sample, time basis,
  filters and coverage. Agreement on some fields does not establish a complete
  record match or a population-wide conclusion.
- Preserve missing, partial, failed, denied and unknown outcomes. Do not turn
  unavailable values into measured zeroes or an empty result into absence beyond
  its query scope. If you summarize execution, distinguish attempts from successful
  calls and include material failures rather than saying everything succeeded.
- Compare or combine measures only when their definitions, time basis, scope and
  coverage support it. Different numbers alone prove neither a cause nor a
  difference in definitions. Label hypotheses and retain plausible alternatives.
- Reconcile contradictions between the summary, tables and caveats. Remove or
  narrow unsupported claims using evidence already obtained; do not make extra
  calls beyond the user's limits just to make the answer look complete.
- Deliver the complete requested result with its citations and material gaps in
  the final answer, including after task-list updates or a citation correction.` + "\n\n" + EvidenceQuoteContract()
}
