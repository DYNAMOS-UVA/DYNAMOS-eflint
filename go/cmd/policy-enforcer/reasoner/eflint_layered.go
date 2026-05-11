package reasoner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
)

// -----------------------------------------------------------------------------
// Fact response helpers
// -----------------------------------------------------------------------------

// eflintFact represents a single fact instance returned by the eFLINT
// `facts` command. The reasoner uses these to derive valid-archetype /
// valid-compute-provider sets without needing to parse the generative-query
// response shape (the `facts` command already returns every grounded fact).
type eflintFact struct {
	FactType   string `json:"fact-type"`
	TaggedType string `json:"tagged-type"`
	Arguments  []struct {
		FactType string `json:"fact-type"`
		Value    string `json:"value"`
	} `json:"arguments"`
}

// fetchFacts sends the "facts" command to the given manager and parses the
// response into a slice of eflintFact values.
func fetchFacts(mgr *eflint.Manager) ([]eflintFact, error) {
	response, err := mgr.SendCommand(`{"command": "facts"}`)
	if err != nil {
		return nil, fmt.Errorf("failed to get facts from eFLINT: %w", err)
	}

	var factsResponse struct {
		Values []eflintFact `json:"values"`
	}
	if err := json.Unmarshal([]byte(response), &factsResponse); err != nil {
		return nil, fmt.Errorf("failed to parse facts response: %w", err)
	}

	return factsResponse.Values, nil
}

// EvaluateRequestApproval implements the layered request-approval evaluation
// (Layer 1 + Layer 2 + Layer 3) on top of the eFLINT instance pool.
//
// Lifecycle for a single evaluation:
//
//  1. Acquire one pool entry. Each entry is started with the Layer-1 interface
//     policy as its boot model, so the base fact types and query-fact
//     declarations are already in place.
//  2. Push the Layer-2 shared rules (intermediate facts + Extend rules).
//  3. Push the per-steward Layer-2 phrases for each steward we have an
//     agreement for. Stewards missing from StewardPhrases stay absent: the
//     reasoner naturally derives permitted-at-steward = false for them.
//  4. Push the Layer-3 request facts: +requester(R). +requested-steward(S)...
//  5. Query permitted-request(R) for the top-level decision.
//  6. For each requested steward, query permitted-at-steward(R, S). If it
//     holds, intersect the relation-allows-* and steward-supports-* base
//     facts to materialise the valid-archetype / valid-compute-provider sets,
//     and fire submit-data-request(R, S) so the data-request fact and
//     obligated-log duty become part of the audit trail.
//  7. Release the pool entry; the pool restarts the eFLINT process with the
//     Layer-1 baseline so no state leaks between requests.
func (r *EflintReasoner) EvaluateRequestApproval(ctx context.Context, params RequestApprovalParams) (*RequestApprovalResult, error) {
	if params.Requester == "" {
		return nil, fmt.Errorf("requester is required")
	}

	entry, err := r.pool.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer func() {
		go r.pool.Release(entry)
	}()
	mgr := entry.Manager

	if strings.TrimSpace(params.SharedRules) != "" {
		if _, err := mgr.SendPhrases(params.SharedRules); err != nil {
			return nil, fmt.Errorf("failed to load Layer-2 shared rules: %w", err)
		}
	}

	for _, steward := range params.Stewards {
		text, ok := params.StewardPhrases[steward]
		if !ok || strings.TrimSpace(text) == "" {
			r.logger.Debug("no Layer-2 phrases for steward; skipping",
				zap.String("steward", steward),
			)
			continue
		}
		if _, err := mgr.SendPhrases(text); err != nil {
			return nil, fmt.Errorf("failed to load Layer-2 agreement for %s: %w", steward, err)
		}
	}

	requesterLit := quoteEflintLiteral(params.Requester)
	layer3 := buildLayer3Phrases(requesterLit, params.Stewards)
	if _, err := mgr.SendPhrases(layer3); err != nil {
		return nil, fmt.Errorf("failed to load Layer-3 request facts: %w", err)
	}

	permittedRequest, err := queryHolds(mgr, fmt.Sprintf("permitted-request(%s)", requesterLit))
	if err != nil {
		return nil, fmt.Errorf("failed to query permitted-request: %w", err)
	}

	result := &RequestApprovalResult{
		PermittedRequest: permittedRequest,
		PerSteward:       make(map[string]StewardDecision, len(params.Stewards)),
	}

	for _, steward := range params.Stewards {
		decision := StewardDecision{}
		if _, hasPhrases := params.StewardPhrases[steward]; !hasPhrases {
			decision.Reason = "no agreement registered"
			result.PerSteward[steward] = decision
			continue
		}

		stewardLit := quoteEflintLiteral(steward)
		permittedAtSteward, err := queryHolds(mgr,
			fmt.Sprintf("permitted-at-steward(%s, %s)", requesterLit, stewardLit))
		if err != nil {
			return nil, fmt.Errorf("failed to query permitted-at-steward(%s): %w", steward, err)
		}
		decision.Permitted = permittedAtSteward
		if !permittedAtSteward {
			decision.Reason = "permitted-at-steward did not hold"
			result.PerSteward[steward] = decision
			continue
		}

		archetypes, computeProviders, err := r.collectValidIntersections(mgr, params.Requester, steward)
		if err != nil {
			return nil, fmt.Errorf("failed to collect valid intersections for %s: %w", steward, err)
		}
		decision.Archetypes = archetypes
		decision.ComputeProviders = computeProviders

		// Fire the submit-data-request act so the data-request fact and the
		// obligated-log duty appear on the execution graph. Failure to fire
		// is non-fatal for the validation outcome but is logged for audit.
		submitPhrase := fmt.Sprintf("submit-data-request(%s, %s).", requesterLit, stewardLit)
		if _, submitErr := mgr.SendPhrases(submitPhrase); submitErr != nil {
			r.logger.Warn("submit-data-request did not fire cleanly",
				zap.String("steward", steward),
				zap.String("requester", params.Requester),
				zap.Error(submitErr),
			)
		}

		result.PerSteward[steward] = decision
	}

	return result, nil
}

// collectValidIntersections derives the matched archetypes / compute providers
// for (requester, steward) by intersecting the relation-allows-* base facts
// with the steward-supports-* base facts. The eFLINT spec defines
// valid-archetype / valid-compute-provider as exactly that conjunction
// (see Layer-2 shared rules), so this Go-side intersection is consistent with
// the reasoner's derivation while avoiding the need to parse the generative
// query response shape.
func (r *EflintReasoner) collectValidIntersections(mgr *eflint.Manager, requester, steward string) ([]string, []string, error) {
	facts, err := fetchFacts(mgr)
	if err != nil {
		return nil, nil, err
	}

	relationArchetypes := filterTernary(facts, "relation-allows-archetype", "requester", requester, "data-steward", steward, "archetype")
	stewardArchetypes := filterBinary(facts, "steward-supports-archetype", "data-steward", steward, "archetype")
	matchedArchetypes := intersectPreservingOrder(relationArchetypes, stewardArchetypes)

	relationComputeProvs := filterTernary(facts, "relation-allows-compute-provider", "requester", requester, "data-steward", steward, "compute-provider")
	stewardComputeProvs := filterBinary(facts, "steward-supports-compute-provider", "data-steward", steward, "compute-provider")
	matchedComputeProvs := intersectPreservingOrder(relationComputeProvs, stewardComputeProvs)

	return matchedArchetypes, matchedComputeProvs, nil
}

// queryHolds runs a single `?Holds(...)` phrase through SendPhrases and parses
// the response. The eFLINT server returns query-results entries that are
// "success" when the queried fact holds and something else (typically
// "failed") otherwise.
func queryHolds(mgr *eflint.Manager, factExpression string) (bool, error) {
	phrase := fmt.Sprintf("?Holds(%s).", factExpression)
	resp, err := mgr.SendPhrases(phrase)
	if err != nil {
		return false, fmt.Errorf("query phrase %q failed: %w", phrase, err)
	}
	if resp == nil {
		return false, fmt.Errorf("nil response for query %q", phrase)
	}
	for _, qr := range resp.QueryResults {
		if strings.EqualFold(strings.TrimSpace(qr), "success") {
			return true, nil
		}
	}
	return false, nil
}

// buildLayer3Phrases produces the Layer-3 request-fact block for one
// evaluation.
func buildLayer3Phrases(requesterLit string, stewards []string) string {
	var b strings.Builder
	b.WriteString("+requester(")
	b.WriteString(requesterLit)
	b.WriteString(").\n")
	seen := map[string]struct{}{}
	for _, st := range stewards {
		if _, ok := seen[st]; ok {
			continue
		}
		seen[st] = struct{}{}
		b.WriteString("+requested-steward(")
		b.WriteString(quoteEflintLiteral(st))
		b.WriteString(").\n")
	}
	return b.String()
}

// quoteEflintLiteral wraps a value as an eFLINT string literal. eFLINT requires
// all fact instance values to be quoted strings — bare atoms are not supported.
// Values containing special characters (e.g. backslashes or double-quotes) are
// escaped so they round-trip correctly through the eFLINT JSON protocol.
func quoteEflintLiteral(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// filterBinary filters `factType(arg0FactType=arg0Value, valueFactType=*)`
// instances and returns the third-position values.
func filterBinary(facts []eflintFact, factType, arg0FactType, arg0Value, valueFactType string) []string {
	var values []string
	for _, fact := range facts {
		if fact.FactType != factType || len(fact.Arguments) < 2 {
			continue
		}
		if fact.Arguments[0].FactType == arg0FactType &&
			fact.Arguments[0].Value == arg0Value &&
			fact.Arguments[1].FactType == valueFactType {
			values = append(values, fact.Arguments[1].Value)
		}
	}
	return values
}

// filterTernary filters `factType(arg0=v0, arg1=v1, valueFactType=*)`
// instances and returns the third-position values.
func filterTernary(facts []eflintFact, factType, arg0FactType, arg0Value, arg1FactType, arg1Value, valueFactType string) []string {
	var values []string
	for _, fact := range facts {
		if fact.FactType != factType || len(fact.Arguments) < 3 {
			continue
		}
		if fact.Arguments[0].FactType == arg0FactType &&
			fact.Arguments[0].Value == arg0Value &&
			fact.Arguments[1].FactType == arg1FactType &&
			fact.Arguments[1].Value == arg1Value &&
			fact.Arguments[2].FactType == valueFactType {
			values = append(values, fact.Arguments[2].Value)
		}
	}
	return values
}

// IntrospectStewardClauses implements the read-only Layer 1 + Layer 2
// loader used by the policy-enforcer HTTP API. It pushes the shared rules
// and the steward's per-steward phrases onto a clean pool instance, asks
// for the full fact set, and groups the relation-allows-* facts per
// requester. No Layer 3 is pushed and no Acts are fired.
func (r *EflintReasoner) IntrospectStewardClauses(ctx context.Context, params IntrospectStewardClausesParams) (*StewardClauses, error) {
	if strings.TrimSpace(params.Steward) == "" {
		return nil, fmt.Errorf("steward is required")
	}
	if strings.TrimSpace(params.StewardPhrases) == "" {
		return nil, fmt.Errorf("no Layer-2 phrases for steward %q", params.Steward)
	}

	entry, err := r.pool.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer func() {
		go r.pool.Release(entry)
	}()
	mgr := entry.Manager

	if strings.TrimSpace(params.SharedRules) != "" {
		if _, err := mgr.SendPhrases(params.SharedRules); err != nil {
			return nil, fmt.Errorf("failed to load Layer-2 shared rules: %w", err)
		}
	}
	if _, err := mgr.SendPhrases(params.StewardPhrases); err != nil {
		return nil, fmt.Errorf("failed to load Layer-2 phrases for %s: %w", params.Steward, err)
	}

	facts, err := fetchFacts(mgr)
	if err != nil {
		return nil, err
	}

	out := &StewardClauses{Steward: params.Steward}
	out.SupportedArchetypes = filterBinary(facts, "steward-supports-archetype",
		"data-steward", params.Steward, "archetype")
	out.SupportedComputeProviders = filterBinary(facts, "steward-supports-compute-provider",
		"data-steward", params.Steward, "compute-provider")

	requesters := requestersWithRelationTo(facts, params.Steward)
	for _, requester := range requesters {
		clauses := RequesterClauses{
			Requester: requester,
			RequestTypes: filterTernary(facts, "relation-allows-request-type",
				"requester", requester, "data-steward", params.Steward, "request-type"),
			Datasets: filterTernary(facts, "relation-allows-dataset",
				"requester", requester, "data-steward", params.Steward, "dataset"),
			Archetypes: filterTernary(facts, "relation-allows-archetype",
				"requester", requester, "data-steward", params.Steward, "archetype"),
			ComputeProviders: filterTernary(facts, "relation-allows-compute-provider",
				"requester", requester, "data-steward", params.Steward, "compute-provider"),
		}
		out.Relations = append(out.Relations, clauses)
	}

	return out, nil
}

// requestersWithRelationTo enumerates the distinct requester values appearing
// in `has-relation(requester, steward)` facts for the given steward, in the
// order they first occur (so the API output is deterministic for a given
// agreement file).
func requestersWithRelationTo(facts []eflintFact, steward string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, fact := range facts {
		if fact.FactType != "has-relation" || len(fact.Arguments) < 2 {
			continue
		}
		if fact.Arguments[0].FactType != "requester" {
			continue
		}
		if fact.Arguments[1].FactType != "data-steward" || fact.Arguments[1].Value != steward {
			continue
		}
		req := fact.Arguments[0].Value
		if _, dup := seen[req]; dup {
			continue
		}
		seen[req] = struct{}{}
		out = append(out, req)
	}
	return out
}

// intersectPreservingOrder returns the intersection of a and b in the order
// they first appear in a, with duplicates removed. Returns nil (not an empty
// slice) when there is no overlap so the result is comparable to nil and
// safely omitted from JSON / wire-format encodings that use omitempty.
func intersectPreservingOrder(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	bSet := make(map[string]struct{}, len(b))
	for _, v := range b {
		bSet[v] = struct{}{}
	}
	seen := make(map[string]struct{}, len(a))
	var out []string
	for _, v := range a {
		if _, ok := bSet[v]; !ok {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
