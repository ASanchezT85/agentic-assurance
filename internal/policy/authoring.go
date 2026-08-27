// Package policy compiles and evaluates deterministic hard policy.
//
// The load-bearing separation is between authoring and execution. Policy is authored
// in YAML (spec section 15.1) and compiled once into a signed bundle. Spec section
// 15.2 forbids interpreting that YAML on every order, so the evaluator in
// evaluate.go touches only compiled structures and never reaches a parser. That is
// asserted, not merely intended: see TestEvaluationNeverParsesSource.
package policy

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"agentic-assurance/internal/intent"
)

// Source is the authored policy document, one-to-one with the YAML in spec 15.1.
type Source struct {
	Version int          `yaml:"version"`
	Policy  string       `yaml:"policy"`
	Rules   []SourceRule `yaml:"rules"`
}

// SourceRule is one authored rule.
//
// `when` narrows which intents a rule applies to; `require` states what must hold;
// `action` is what happens when the rule matches and its requirement fails. A rule
// with no `require` fires on `when` alone, which is how OPTIONS_DISABLED in the spec
// example works.
type SourceRule struct {
	ID      string     `yaml:"id"`
	When    SourceWhen `yaml:"when"`
	Require SourceReq  `yaml:"require"`
	Action  string     `yaml:"action"`
}

type SourceWhen struct {
	AssetClass    string   `yaml:"asset_class"`
	Side          string   `yaml:"side"`
	OrderType     string   `yaml:"order_type"`
	Instrument    string   `yaml:"instrument_id"`
	Instruments   []string `yaml:"instrument_ids"`
	NotionalGT    *float64 `yaml:"notional_gt"`
	NotionalGTE   *float64 `yaml:"notional_gte"`
	NotionalLT    *float64 `yaml:"notional_lt"`
	NotionalLTE   *float64 `yaml:"notional_lte"`
	ExtendedHours *bool    `yaml:"extended_hours"`
}

type SourceReq struct {
	NotionalLTE *float64 `yaml:"notional_lte"`
	NotionalGTE *float64 `yaml:"notional_gte"`
}

// ParseSource reads an authored policy document.
//
// KnownFields is on: an unrecognized key in a policy file is a typo that would
// otherwise silently disable a rule, and a silently disabled financial control is the
// worst failure mode this package has.
func ParseSource(raw []byte) (*Source, error) {
	var src Source
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&src); err != nil {
		return nil, fmt.Errorf("policy source is not valid YAML: %w", err)
	}
	return &src, nil
}

// Action is a control action (spec section 16).
type Action string

const (
	ActionAllow           Action = "ALLOW"
	ActionObserve         Action = "OBSERVE"
	ActionDelay           Action = "DELAY"
	ActionThrottle        Action = "THROTTLE"
	ActionRequireApproval Action = "REQUIRE_APPROVAL"
	ActionDeny            Action = "DENY"

	// Fleet-level actions from section 16. They are named here so the compiler can
	// reject them with a reason rather than an "unknown action", but an order-level
	// rule cannot produce one: fleet containment is Phase 13, and it is authorized
	// by customer policy rather than emitted by a per-order evaluation (INV-009).
	ActionIsolateCohort Action = "ISOLATE_COHORT"
	ActionReadOnly      Action = "READ_ONLY"
)

// severity orders actions from least to most restrictive.
//
// Resolution across matching rules takes the most restrictive, not the first match.
// First-match makes the outcome depend on the order rules happen to be written in,
// which means reordering a file silently changes what is enforced.
var severity = map[Action]int{
	ActionAllow:           0,
	ActionObserve:         1,
	ActionDelay:           2,
	ActionThrottle:        3,
	ActionRequireApproval: 4,
	ActionDeny:            5,
}

// MoreRestrictive reports whether a is stricter than b.
func MoreRestrictive(a, b Action) bool { return severity[a] > severity[b] }

func orderLevelAction(raw string) (Action, error) {
	a := Action(strings.ToUpper(strings.TrimSpace(raw)))
	if _, ok := severity[a]; ok {
		return a, nil
	}
	switch a {
	case ActionIsolateCohort, ActionReadOnly:
		return "", fmt.Errorf("%s is a fleet-level control action; an order-level rule "+
			"cannot emit one (spec section 16, INV-009, Phase 13)", a)
	case "":
		return "", fmt.Errorf("action is required")
	default:
		return "", fmt.Errorf("unknown action %q", raw)
	}
}

func parseAssetClass(raw string) (intent.AssetClass, error) {
	switch c := intent.AssetClass(strings.ToUpper(strings.TrimSpace(raw))); c {
	case intent.AssetEquity, intent.AssetETF, intent.AssetOption, intent.AssetCrypto:
		return c, nil
	default:
		return "", fmt.Errorf("unknown asset_class %q", raw)
	}
}

func parseSide(raw string) (intent.Side, error) {
	switch s := intent.Side(strings.ToUpper(strings.TrimSpace(raw))); s {
	case intent.SideBuy, intent.SideSell:
		return s, nil
	default:
		return "", fmt.Errorf("unknown side %q", raw)
	}
}

func parseOrderType(raw string) (intent.OrderType, error) {
	switch o := intent.OrderType(strings.ToUpper(strings.TrimSpace(raw))); o {
	case intent.OrderMarket, intent.OrderLimit, intent.OrderStop, intent.OrderStopLimit:
		return o, nil
	default:
		return "", fmt.Errorf("unknown order_type %q", raw)
	}
}
