package policy

import (
	"agentic-assurance/internal/money"
	"fmt"
	"strings"
	"time"
)

// CompileError collects everything wrong with an authored policy.
//
// Compilation reports all problems at once for the same reason envelope validation
// does: an author fixing a policy file should not have to compile ten times to find
// ten mistakes.
type CompileError struct {
	Problems []string
}

func (e *CompileError) Error() string {
	return "policy did not compile:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Validate checks an authored policy without producing a bundle. It is the VALIDATE
// step of the lifecycle, usable in an editor or a pre-commit hook.
func Validate(src *Source) error {
	_, err := compileRules(src)
	return err
}

// Compile turns an authored policy into an executable bundle in COMPILE status.
//
// The result contains no YAML and no unparsed strings. Everything the evaluator
// needs is decided here, once, rather than on every order (spec section 15.2).
func Compile(src *Source, tenantID, bundleID string, at time.Time) (*Bundle, error) {
	if src == nil {
		return nil, &CompileError{Problems: []string{"no policy source"}}
	}

	problems := []string{}
	if strings.TrimSpace(tenantID) == "" {
		problems = append(problems, "tenant_id is required")
	}
	if strings.TrimSpace(bundleID) == "" {
		problems = append(problems, "bundle_id is required")
	}
	if strings.TrimSpace(src.Policy) == "" {
		problems = append(problems, "policy name is required")
	}
	if src.Version < 1 {
		problems = append(problems, "version must be 1 or greater; there is no unversioned policy (INV-010)")
	}

	rules, err := compileRules(src)
	if err != nil {
		var ce *CompileError
		if ok := asCompileError(err, &ce); ok {
			problems = append(problems, ce.Problems...)
		} else {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return nil, &CompileError{Problems: problems}
	}

	return &Bundle{
		BundleID:   bundleID,
		TenantID:   tenantID,
		Policy:     src.Policy,
		Version:    src.Version,
		Rules:      rules,
		CompiledAt: at.UTC(),
		Activation: Activation{Status: StatusCompiled},
	}, nil
}

func compileRules(src *Source) ([]CompiledRule, error) {
	if src == nil {
		return nil, &CompileError{Problems: []string{"no policy source"}}
	}

	var problems []string
	if len(src.Rules) == 0 {
		problems = append(problems, "a policy with no rules enforces nothing")
	}

	seen := map[string]bool{}
	out := make([]CompiledRule, 0, len(src.Rules))

	for i, r := range src.Rules {
		where := fmt.Sprintf("rule %d", i)
		if r.ID != "" {
			where = "rule " + r.ID
		}

		switch {
		case strings.TrimSpace(r.ID) == "":
			problems = append(problems, where+": id is required; a decision must name the rule that produced it")
		case seen[r.ID]:
			problems = append(problems, where+": duplicate rule id")
		default:
			seen[r.ID] = true
		}

		compiled := CompiledRule{ID: r.ID}

		action, err := orderLevelAction(r.Action)
		if err != nil {
			problems = append(problems, where+": "+err.Error())
		} else {
			compiled.Action = action
		}

		if r.When.AssetClass != "" {
			c, err := parseAssetClass(r.When.AssetClass)
			if err != nil {
				problems = append(problems, where+": when: "+err.Error())
			}
			compiled.WhenAssetClass = c
		}
		if r.When.Side != "" {
			s, err := parseSide(r.When.Side)
			if err != nil {
				problems = append(problems, where+": when: "+err.Error())
			}
			compiled.WhenSide = s
		}
		if r.When.OrderType != "" {
			o, err := parseOrderType(r.When.OrderType)
			if err != nil {
				problems = append(problems, where+": when: "+err.Error())
			}
			compiled.WhenOrderType = o
		}

		compiled.WhenInstruments = mergeInstruments(r.When.Instrument, r.When.Instruments)
		compiled.WhenNotionalGT = r.When.NotionalGT
		compiled.WhenNotionalGTE = r.When.NotionalGTE
		compiled.WhenNotionalLT = r.When.NotionalLT
		compiled.WhenNotionalLTE = r.When.NotionalLTE
		compiled.WhenExtendedHours = r.When.ExtendedHours
		compiled.RequireNotionalLTE = r.Require.NotionalLTE
		compiled.RequireNotionalGTE = r.Require.NotionalGTE

		compiled.NeedsNotional = compiled.WhenNotionalGT != nil || compiled.WhenNotionalGTE != nil ||
			compiled.WhenNotionalLT != nil || compiled.WhenNotionalLTE != nil ||
			compiled.RequireNotionalLTE != nil || compiled.RequireNotionalGTE != nil

		problems = append(problems, boundsProblems(where, compiled)...)
		out = append(out, compiled)
	}

	if len(problems) > 0 {
		return nil, &CompileError{Problems: problems}
	}
	return out, nil
}

// boundsProblems catches thresholds that can never fire. A rule that cannot match is
// a control someone believes they have.
func boundsProblems(where string, r CompiledRule) []string {
	var problems []string

	for name, v := range map[string]*money.Amount{
		"when.notional_gt":     r.WhenNotionalGT,
		"when.notional_gte":    r.WhenNotionalGTE,
		"when.notional_lt":     r.WhenNotionalLT,
		"when.notional_lte":    r.WhenNotionalLTE,
		"require.notional_lte": r.RequireNotionalLTE,
		"require.notional_gte": r.RequireNotionalGTE,
	} {
		if v != nil && *v < 0 {
			problems = append(problems, fmt.Sprintf("%s: %s is negative", where, name))
		}
	}

	if r.WhenNotionalGT != nil && r.WhenNotionalLT != nil && *r.WhenNotionalGT >= *r.WhenNotionalLT {
		problems = append(problems, where+": when.notional_gt is not below when.notional_lt, so the rule can never match")
	}
	if r.WhenNotionalGTE != nil && r.WhenNotionalLTE != nil && *r.WhenNotionalGTE > *r.WhenNotionalLTE {
		problems = append(problems, where+": when.notional_gte exceeds when.notional_lte, so the rule can never match")
	}
	if r.RequireNotionalLTE != nil && r.RequireNotionalGTE != nil && *r.RequireNotionalGTE > *r.RequireNotionalLTE {
		problems = append(problems, where+": require bounds are contradictory, so the requirement can never hold")
	}

	// Sorting keeps compiler output deterministic across runs, since the loop above
	// ranges a map.
	sortStrings(problems)
	return problems
}

func mergeInstruments(single string, many []string) []string {
	var out []string
	if strings.TrimSpace(single) != "" {
		out = append(out, single)
	}
	for _, m := range many {
		if strings.TrimSpace(m) != "" {
			out = append(out, m)
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func asCompileError(err error, target **CompileError) bool {
	ce, ok := err.(*CompileError)
	if ok {
		*target = ce
	}
	return ok
}
